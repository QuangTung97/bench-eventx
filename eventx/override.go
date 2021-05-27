package eventx

import (
	"bench/benchpb"
	"google.golang.org/protobuf/proto"
)

// UnmarshalledEvent for user defined events
type UnmarshalledEvent struct {
	*benchpb.Event
}

func unmarshalEvent(e Event) UnmarshalledEvent {
	event := &benchpb.Event{}
	err := proto.Unmarshal([]byte(e.Data), event)
	if err != nil {
		panic(err)
	}
	event.Id = e.ID
	event.Seq = e.Seq

	return UnmarshalledEvent{
		Event: event,
	}
}

func (e UnmarshalledEvent) getSequence() uint64 {
	return e.Seq
}
