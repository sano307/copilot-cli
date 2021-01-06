package stream

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/cloudformation"
)

const (
	stackFetchIntervalDuration = 3 * time.Second // How long to wait until Fetch is called again for a StackStreamer.
)

// StackEventsDescriber is the CloudFormation interface needed to describe stack events.
type StackEventsDescriber interface {
	DescribeStackEvents(*cloudformation.DescribeStackEventsInput) (*cloudformation.DescribeStackEventsOutput, error)
}

// StackEvent is a CloudFormation stack event.
type StackEvent struct {
	LogicalResourceID    string
	ResourceType         string
	ResourceStatus       string
	ResourceStatusReason string
}

// StackStreamer is a FetchNotifyStopper for StackEvent events started by a change set.
type StackStreamer struct {
	client                StackEventsDescriber
	stackName             string
	changeSetCreationTime time.Time

	subscribers   []chan StackEvent
	pastEventIDs  map[string]bool
	eventsToFlush []StackEvent
}

// NewStackStreamer creates a StackStreamer from a cloudformation client, stack name, and the change set creation timestamp.
func NewStackStreamer(cfn StackEventsDescriber, stackName string, csCreationTime time.Time) *StackStreamer {
	return &StackStreamer{
		client:                cfn,
		stackName:             stackName,
		changeSetCreationTime: csCreationTime,
		pastEventIDs:          make(map[string]bool),
	}
}

// Subscribe registers the channels to receive notifications from the streamer.
func (s *StackStreamer) Subscribe(channels ...chan StackEvent) {
	s.subscribers = append(s.subscribers, channels...)
}

// Fetch retrieves and stores any new CloudFormation stack events since the ChangeSetCreationTime in chronological order.
// If an error occurs from describe stack events, returns a wrapped error.
// Otherwise, returns the time the next Fetch should be attempted.
func (s *StackStreamer) Fetch() (next time.Time, err error) {
	var events []StackEvent
	var nextToken *string
	for {
		// DescribeStackEvents returns events in reverse chronological order,
		// so we retrieve new events until we go past the ChangeSetCreationTime or we see an already seen event ID.
		// This logic is taken from the AWS CDK:
		// https://github.com/aws/aws-cdk/blob/43f3f09cc561fd32d651b2c327e877ad81c2ddb2/packages/aws-cdk/lib/api/util/cloudformation/stack-activity-monitor.ts#L230-L234
		out, err := s.client.DescribeStackEvents(&cloudformation.DescribeStackEventsInput{
			NextToken: nextToken,
			StackName: aws.String(s.stackName),
		})
		if err != nil {
			return next, fmt.Errorf("describe stack events %s: %w", s.stackName, err)
		}

		var finished bool
		for _, event := range out.StackEvents {
			if event.Timestamp.Before(s.changeSetCreationTime) {
				finished = true
				break
			}
			if _, seen := s.pastEventIDs[aws.StringValue(event.EventId)]; seen {
				finished = true
				break
			}
			events = append(events, StackEvent{
				LogicalResourceID:    aws.StringValue(event.LogicalResourceId),
				ResourceType:         aws.StringValue(event.ResourceType),
				ResourceStatus:       aws.StringValue(event.ResourceStatus),
				ResourceStatusReason: aws.StringValue(event.ResourceStatusReason),
			})
			s.pastEventIDs[aws.StringValue(event.EventId)] = true
		}
		if finished || out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}

	// Store events to flush in chronological order.
	reverse(events)
	s.eventsToFlush = append(s.eventsToFlush, events...)
	return time.Now().Add(stackFetchIntervalDuration), nil
}

// Notify flushes all new events to the streamer's subscribers.
func (s *StackStreamer) Notify() {
	for _, event := range s.eventsToFlush {
		for _, sub := range s.subscribers {
			sub <- event
		}
	}
	s.eventsToFlush = nil // reset after flushing all events.
}

// Stop closes all subscribed channels notifying them that no more events will be sent.
func (s *StackStreamer) Stop() {
	for _, sub := range s.subscribers {
		close(sub)
	}
}

// Taken from https://github.com/golang/go/wiki/SliceTricks#reversing
func reverse(arr []StackEvent) {
	for i := len(arr)/2 - 1; i >= 0; i-- {
		opp := len(arr) - 1 - i
		arr[i], arr[opp] = arr[opp], arr[i]
	}
}
