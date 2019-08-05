package metadata

import (
	"context"
	"fmt"
	"os"
	"testing"

	"cloud.google.com/go/functions/metadata"
)

func TestUpload(t *testing.T) {
	event := StorageEvent{
		fmt.Sprintf("workflow.%s.log", os.Getenv("WORKFLOW_ID")),
	}
	SetCachedToken(os.Getenv("TOKEN"))
	if err := Upload(getContext(), event); err != nil {
		t.Error(err)
	}
}

func getContext() context.Context {
	return metadata.NewContext(context.Background(), &metadata.Metadata{
		EventType: "google.storage.object.finalize",
	})
}
