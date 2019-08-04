package main

import (
	"bufio"
	"context"
	"flag"
	"log"
	"os"
	"sync"

	"cloud.google.com/go/pubsub"
)

func main() {
	projectID := flag.String("project", "", "Google project ID for PubSub topic")
	topicID := flag.String("topic", "", "PubSub topic ID")
	logType := flag.String("type", "", "Log type: EPI or HCA")
	flag.Parse()

	ctx := context.Background()

	client, err := pubsub.NewClient(ctx, *projectID)
	if err != nil {
		log.Fatal(err)
	}
	topic := client.Topic(*topicID)

	var wg sync.WaitGroup
	errs := make(chan error, 1)

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		path := scanner.Text()
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := newMessage(path, *logType)
			if _, err := topic.Publish(ctx, m).Get(ctx); err != nil {
				errs <- err
			}
		}()
	}
	go func() {
		wg.Wait()
		errs <- nil
	}()
	if err = <-errs; err != nil {
		log.Fatal(err)
	}
}

func newMessage(path string, logType string) *pubsub.Message {
	return &pubsub.Message{
		Data: []byte(path),
		Attributes: map[string]string{
			"type": logType,
		},
	}
}
