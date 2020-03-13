package main

import (
	"encoding/json"
	"fmt"
	"github.com/olekukonko/tablewriter"
	"github.com/skratchdot/open-golang/open"
	"github.com/urfave/cli"
	"io/ioutil"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) == 2 {
		_, err := strconv.Atoi(os.Args[1])
		if err == nil {
			err := open.Start("http://github.com/apache/hadoop-ozone/pull/" + os.Args[1])
			if err != nil {
				log.Fatal(err)
			}
			return
		}
	}

	app := cli.NewApp()
	app.Name = "ogh"
	app.Usage = "Ozone Github Development helper"
	app.Commands = []*cli.Command{
		{
			Name:    "review",
			Aliases: []string{"r"},
			Usage:   "Show the review queue (all READY pull requests)",
			Action: func(c *cli.Context) error {
				return run(false)
			},
		},
		{
			Name:    "pull-requests",
			Aliases: []string{"pr"},
			Usage:   "Show all the available pull requests",
			Action: func(c *cli.Context) error {
				return run(true)
			},
		},
		{
			Name:    "builds",
			Aliases: []string{"b"},
			Usage:   "Print out github action related information",
			Subcommands: []*cli.Command{
				{
					Name:  "master",
					Usage: "Show all the available pull requests",
					Subcommands: []*cli.Command{

					},
					Action: func(c *cli.Context) error {
						return listBuilds("apache", "master", 8247)
					},
				},
				{
					Name:  "fork",
					Usage: "Show the builds from a specified forked repository",
					Flags: []&cli.Flag{
						cli.StringFlag{
							Name:  "user",
							Usage: "Github user name",
						},
					},
					Subcommands: []*cli.Command{

					},
					Action: func(c *cli.Context) error {
						return listBuilds(c.String("user"), "", -1)
					},
				},
			},
		},
	}
	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}

}

type getter func() ([]byte, error)

func cachedGet(getter getter, key string) ([]byte, error) {
	oghCache := os.Getenv("OGH_CACHE")
	cacheFile := oghCache + "." + key
	if oghCache != "" {
		if stat, err := os.Stat(cacheFile); !os.IsNotExist(err) {
			if stat.ModTime().Add(3 * time.Minute).After(time.Now()) {
				return ioutil.ReadFile(cacheFile)
			}
		}
	}
	result, err := getter()
	if oghCache != "" {
		err = ioutil.WriteFile(cacheFile, result, 0600)
		if err != nil {
			return nil, err
		}
	}
	return result, err
}

func run(all bool) error {
	var key string
	if all {
		key = "pr"
	} else {
		key = "review"
	}
	body, err := cachedGet(readGithubApiV4, key)
	if err != nil {
		return err
	}

	result := make(map[string]interface{})
	json.Unmarshal(body, &result)

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"ID", "Author", "Summary", "Participants", "Check"})
	table.SetAutoWrapText(false)
	prs := m(result, "data", "repository", "pullRequests", "edges")

	for _, prNode := range l(prs) {

		pr := m(prNode, "node")

		if !all && !ready(pr) {
			continue
		}

		author := ms(pr, "author", "login")
		participants := getParticipants(pr, author)
		mergeableMark := ""
		if ms(pr, "mergeable") == "CONFLICTING" {
			mergeableMark = "[C] "
		}
		table.Append([]string{
			fmt.Sprintf("%d", int(m(pr, "number").(float64))),
			">" + limit(author, 12),
			limit(mergeableMark+ms(pr, "title"), 50),
			limit(strings.Join(participants, ","), 35),
			buildStatus(pr),
		})
	}
	table.Render() // Send output

	return nil
}

func ready(pr interface{}) bool {
	if ms(pr, "mergeable") == "CONFLICTING" {
		return false
	}
	for _, commitEdge := range l(m(pr, "commits", "edges")) {
		commit := m(commitEdge, "node", "commit")
		for _, suite := range l(m(commit, "checkSuites", "edges")) {
			for _, runs := range l(m(suite, "node", "checkRuns", "edges")) {
				conclusion := ms(runs, "node", "conclusion")
				if conclusion == "FAILURE" || conclusion == "CANCELLED" {
					return false
				}
			}
		}
		break
	}

	for _, review := range lastReviewsPerUser(pr) {
		state := ms(review, "state")
		if state == "CHANGES_REQUESTED" {
			return false
		}
	}

	return true
}

func getParticipants(pr interface{}, author string) []string {
	reviews := lastReviewsPerUser(pr)

	participants := make([]string, 0)

	participants = append(participants, filterReviews(reviews, "CHANGES_REQUESTED", "✕")...)
	participants = append(participants, filterReviews(reviews, "APPROVED", "✓")...)
	participants = append(participants, filterReviews(reviews, "COMMENTED", "")...)

	for _, participant := range l(m(pr, "participants", "edges")) {
		login := ms(participant, "node", "login")
		if _, ok := reviews[login]; !ok && login != author {
			participants = append(participants, limit(login, 5))
		}
	}
	return participants
}

func lastReviewsPerUser(pr interface{}) map[string]interface{} {
	reviewers := make(map[string]interface{})
	for _, review := range l(m(pr, "reviews", "nodes")) {
		author := ms(review, "author", "login")
		if last_review, found := reviewers[author]; found {

			oldRecord, err := time.Parse(time.RFC3339, ms(last_review, "updatedAt"))
			if err != nil {
				panic(err)
			}

			newRecord, err := time.Parse(time.RFC3339, ms(review, "updatedAt"))
			if err != nil {
				panic(err)
			}

			if oldRecord.Before(newRecord) {
				reviewers[author] = review
			}

		} else {
			reviewers[author] = review
		}
	}
	return reviewers
}

func filterReviews(reviews map[string]interface{}, status string, symbol string) []string {
	result := make([]string, 0)
	for _, review := range reviews {
		state := ms(review, "state")
		if state == status {
			result = append(result, symbol+limit(strings.ToUpper(ms(review, "author", "login")), 5))
		}
	}
	return result
}

type statusTransform struct {
	position int
	abbrev   byte
}
