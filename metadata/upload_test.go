package metadata

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/functions/metadata"
)

func TestUpload(t *testing.T) {

	cachedToken = CachedToken{
		os.Getenv("TOKEN"),
		time.Now().Add(time.Second * tokenLifetimeSec),
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		workflowID := scanner.Text()
		event := StorageEvent{
			fmt.Sprintf("workflow.%s.log", workflowID),
		}
		if err := Upload(getContext(), event); err != nil {
			t.Error(err)
		}
	}
}

func getContext() context.Context {
	return metadata.NewContext(context.Background(), &metadata.Metadata{
		EventType: "google.storage.object.finalize",
	})
}
