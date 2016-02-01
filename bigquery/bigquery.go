package main

import (
	"time"

	"golang.org/x/net/context"
	"golang.org/x/oauth2/google"
	"google.golang.org/cloud/bigquery"
)

type BigQueryJobStatus struct {
	Id          string
	Label       string
	Nanoseconds int64
}

type BatchStatus struct {
	File CSVFile
	Jobs []BigQueryJobStatus
}

func loadCSVIntoBigQuery(projectId string, datasetId string, tableId string, csvC <-chan CSVFile) (<-chan BatchStatus, <-chan error) {
	errC := make(chan error)
	batchStatusC := make(chan BatchStatus)

	go func() {
		for f := range csvC {
			go func(file CSVFile) {
				status := BatchStatus{
					File: file,
					Jobs: make([]BigQueryJobStatus, 1),
				}
				bqClient, err := bigQueryClient(projectId)
				if err != nil {
					errC <- err
					return
				}

				dupTableId := tableId + "_with_dups"
				status.Jobs[0], err = loadSingleCSVFileIntoBigQuery(
					bqClient, projectId,
					file.Bucket, file.Object,
					datasetId, dupTableId,
				)
				if err != nil {
					errC <- err
				}

				batchStatusC <- status
			}(f)
		}
	}()

	return batchStatusC, errC
}

func loadSingleCSVFileIntoBigQuery(client *bigquery.Client, projectId string, bucketName string, objectName string, datasetId string, tableId string) (BigQueryJobStatus, error) {

	table := bigquery.Table{
		ProjectID: projectId,
		DatasetID: datasetId,
		TableID:   tableId,
	}

	gcs := client.NewGCSReference("gs://" + bucketName + "/" + objectName)

	job, err := client.Copy(
		context.Background(), &table, gcs,
		bigquery.CreateDisposition(bigquery.CreateIfNeeded),
		bigquery.DestinationSchema(snapshotSchema()),
		bigquery.MaxBadRecords(1),
	)
	status := BigQueryJobStatus{
		Id:    job.ID(),
		Label: "csv.load." + tableId,
	}
	if err != nil {
		return status, err
	}

	wait, err := waitForJobCompletion(job)
	status.Nanoseconds = wait.Nanoseconds()
	if err != nil {
		return status, err
	}

	return status, nil
}

func waitForJobCompletion(job *bigquery.Job) (time.Duration, error) {
	startTime := time.Now()
	for range time.Tick(time.Second * 5) {
		s, err := job.Status(context.Background())
		if err != nil {
			return time.Now().Sub(startTime), err
		}
		if !s.Done() {
			continue
		}
		if err := s.Err(); err != nil {
			return time.Now().Sub(startTime), err
		}
		break
	}
	return time.Now().Sub(startTime), nil
}

func snapshotSchema() bigquery.Schema {
	return bigquery.Schema{
		&bigquery.FieldSchema{
			Name:     "insertId",
			Required: true,
			Type:     bigquery.StringFieldType,
		},
		&bigquery.FieldSchema{
			Name:     "metricId",
			Required: true,
			Type:     bigquery.StringFieldType,
		},
		&bigquery.FieldSchema{
			Name:     "url",
			Required: true,
			Type:     bigquery.StringFieldType,
		},
		&bigquery.FieldSchema{
			Name:     "lat",
			Required: false,
			Type:     bigquery.FloatFieldType,
		},
		&bigquery.FieldSchema{
			Name:     "lg",
			Required: false,
			Type:     bigquery.FloatFieldType,
		},
		&bigquery.FieldSchema{
			Name:     "type",
			Required: true,
			Type:     bigquery.StringFieldType,
		},
		&bigquery.FieldSchema{
			Name:     "unit",
			Required: false,
			Type:     bigquery.StringFieldType,
		},
		&bigquery.FieldSchema{
			Name:     "timestamp",
			Required: true,
			Type:     bigquery.TimestampFieldType,
		},
		&bigquery.FieldSchema{
			Name:     "value",
			Required: true,
			Type:     bigquery.FloatFieldType,
		},
	}
}

func bigQueryClient(projectId string) (*bigquery.Client, error) {
	client, err := google.DefaultClient(context.Background(), bigquery.Scope)
	if err != nil {
		return nil, err
	}
	return bigquery.NewClient(client, projectId)
}
