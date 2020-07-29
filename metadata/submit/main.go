package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"cloud.google.com/go/functions/metadata"

	meta "github.com/broadinstitute/cromwell-task-monitor-bq/metadata"
)

func main() {

	meta.SetCachedToken(&meta.Token{
		AccessToken: os.Getenv("CROMWELL_ACCESS_TOKEN"),
		ExpiresIn:   int(time.Duration(time.Hour).Seconds()),
	})

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {

		workflowID := scanner.Text()
		log.Println(workflowID)

		event := meta.StorageEvent{
			Path: fmt.Sprintf("workflow.%s.log", workflowID),
		}
		if err := meta.Upload(getContext(), event); err != nil {
			log.Fatal(err)
		}
	}
}

func getContext() context.Context {
	return metadata.NewContext(context.Background(), &metadata.Metadata{
		EventType: "google.storage.object.finalize",
	})
}
