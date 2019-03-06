package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/rainchasers/com.rainchasers.gauge/daemon"
	"github.com/rainchasers/com.rainchasers.gauge/gauge"
	"github.com/rainchasers/com.rainchasers.gauge/queue"
	"github.com/rainchasers/report"
)

// Responds to environment variables:
//   PROJECT_ID (no default, blank for validation mode)
//   PUBSUB_TOPIC (no default, blank for validation mode)
func main() {
	d := daemon.New("sepa", 7*24*time.Hour)
	d.Run(run)
	d.Close()

	if err := d.Err(); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

func run(d *daemon.Supervisor) error {
	// parse env vars
	projectID := os.Getenv("PROJECT_ID")
	topicName := os.Getenv("PUBSUB_TOPIC")
	isDryRun := projectID == ""

	// discover SEPA gauging stations
	stations, err := discover(d)
	if err != nil {
		return err
	}

	// calculate update rate to refresh on schedule
	refreshPeriodInSeconds := 15 * 60
	every := calculateRate(len(stations), refreshPeriodInSeconds)
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	// if dry run, shorten running model
	if isDryRun {
		stations = stations[0:5]
		go func() {
			<-time.After(30 * time.Second)
			d.Close()
		}()
		ticker = time.NewTicker(time.Second)
		defer ticker.Stop()
	}

	// open connection to pubsub
	topic, err := queue.New(d, projectID, topicName)
	if err != nil {
		return err
	}
	defer topic.Stop()

	// get readings & publish them to pubsub
	nConsecutiveErr := 0
	n := 0
updateLoop:
	for {
		i := n % len(stations)

		err := func() (err error) {
			ctx, cancel := context.WithCancel(d.Trace(d.Context()))
			ctx = d.StartSpan(ctx, "station.updated")
			defer func() {
				d.EndSpan(ctx, err, report.Data{
					"station": stations[i].UUID(),
				})
				cancel()
			}()

			// TODO: parent span ctx not propagated
			readings, err := getReadings(d, stations[i].DataURL)
			if err != nil {
				return err
			}

			// TODO: parent span ctx not propagated
			return topic.Publish(d, &gauge.Snapshot{
				Station:  stations[i],
				Readings: readings,
			})
		}()

		if err != nil {
			nConsecutiveErr++
			if nConsecutiveErr > 5 {
				// ignore a few isolated errors, but if
				// many consecutive bubble up to restart
				return err
			}
		} else {
			nConsecutiveErr = 0
		}

		n++
		select {
		case <-ticker.C:
		case <-d.Context().Done():
			break updateLoop
		}
	}

	// validate log stream
	if d.Count("station.discovered") != 1 {
		return errors.New("discovered event expected but not present")
	}
	if d.Count("station.updated") < 1 {
		return errors.New("station.updated event expected but not present")
	}

	return nil
}

func calculateRate(n int, seconds int) time.Duration {
	maxPerSecond := 1
	ms := seconds * 1000 / n
	min := 1000 / maxPerSecond
	if ms < min {
		ms = min
	}
	return time.Millisecond * time.Duration(ms)
}