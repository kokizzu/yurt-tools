package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/nomad/api"
	"github.com/heroku/docker-registry-client/registry"
)

var (
	pageinfo pagedata
)

type pagedata struct {
	TaskList []task
	Updated  time.Time
}

type task struct {
	Name    string
	Image   string
	Version string
	Newer   []string
	NoData  bool
}

func getTasksFromNomad() ([]task, error) {
	c, err := api.NewClient(api.DefaultConfig())
	if err != nil {
		return nil, err
	}

	list, _, err := c.Jobs().List(nil)
	if err != nil {
		return nil, err
	}

	tasklist := []task{}
	for _, i := range list {
		t := task{}
		if i.Stop || i.Type == api.JobTypeBatch {
			continue
		}
		t.Name = i.Name

		job, _, err := c.Jobs().Info(i.ID, nil)
		if err != nil {
			log.Println(err)
			continue
		}
		for _, taskGroup := range job.TaskGroups {
			for _, task := range taskGroup.Tasks {
				t.Image = task.Config["image"].(string)
				parts := strings.SplitN(t.Image, ":", 2)
				if len(parts) != 2 {
					log.Printf("Task %s has invalid tag: %s", t.Name, t.Image)
					t.Version = "0.0.0"
				} else {
					t.Image = parts[0]
					t.Version = parts[1]
				}
			}
			tasklist = append(tasklist, t)
		}
	}
	return tasklist, nil
}

func getTagsForImage(repo string) ([]string, error) {
	url := "https://registry-1.docker.io/"
	username := os.Getenv("UP2DATE_REGISTRY_USERNAME")
	password := os.Getenv("UP2DATE_REGISTRY_PASSWORD")
	hub, err := registry.New(url, username, password)
	if err != nil {
		return nil, err
	}
	tags, err := hub.Tags(repo)
	if err != nil {
		return nil, err
	}
	return tags, nil
}

func getNewerVersions(tl []task) ([]task, error) {
	out := make([]task, len(tl))
	for i, task := range tl {
		out[i] = tl[i]
		have, err := version.NewVersion(task.Version)
		if err != nil {
			log.Printf("Task %s has uncomparable version: %s", task.Name, err)
			out[i].NoData = true
			continue
		}
		tags, err := getTagsForImage(task.Image)
		if err != nil {
			log.Println(err)
			out[i].NoData = true
			continue
		}

		versions := []*version.Version{}
		for i := range tags {
			v, err := version.NewVersion(tags[i])
			if err != nil {
				continue
			}
			versions = append(versions, v)
		}
		sort.Sort(sort.Reverse(version.Collection(versions)))

		for _, v := range versions {
			if err != nil {
				log.Println("Attempted to parse unparseable version", task.Name, err)
				continue
			}
			if have.LessThan(v) {
				out[i].Newer = append(out[i].Newer, v.Original())
			}
			if len(out[i].Newer) > 5 {
				break
			}
		}
	}
	return out, nil
}

func updateData() {
	tasklist, err := getTasksFromNomad()
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}

	tasklist, err = getNewerVersions(tasklist)
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}

	pageinfo.TaskList = tasklist
	pageinfo.Updated = time.Now()
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	t := template.Must(template.ParseFiles("status.tpl"))
	t.Execute(w, pageinfo)
}

func main() {
	go func() {
		for range time.Tick(time.Hour * 4) {
			updateData()
		}
	}()
	updateData()

	http.HandleFunc("/", statusHandler)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
