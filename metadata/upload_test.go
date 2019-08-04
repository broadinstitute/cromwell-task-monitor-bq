package metadata

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/functions/metadata"
)

func TestUpload(t *testing.T) {
	event := StorageEvent{
		fmt.Sprintf("workflow.%s.log", os.Getenv("WORKFLOW_ID")),
	}
	cachedToken = CachedToken{
		os.Getenv("TOKEN"),
		time.Now().Add(time.Second * tokenLifetimeSec),
	}
	if err := Upload(getContext(), event); err != nil {
		t.Error(err)
	}
}

func getContext() context.Context {
	return metadata.NewContext(context.Background(), &metadata.Metadata{
		EventType: "google.storage.object.finalize",
	})
}
