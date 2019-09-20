package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"

	"cloud.google.com/go/functions/metadata"

	meta "github.com/broadinstitute/cromwell-task-monitor-bq/metadata"
)

func main() {

	meta.SetCachedToken(os.Getenv("TOKEN"))

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
