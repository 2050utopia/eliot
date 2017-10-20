package api

import (
	"io"
	"syscall"

	pods "github.com/ernoaapa/can/pkg/api/services/pods/v1"
)

// Client interface for all API clients
type Client interface {
	GetPods() ([]*pods.Pod, error)
	GetPod(podName string) (*pods.Pod, error)
	CreatePod(pod *pods.Pod, opts ...PodOpts) error
	StartPod(name string) (*pods.Pod, error)
	DeletePod(pod *pods.Pod) (*pods.Pod, error)
	Attach(containerID string, attachIO AttachIO, hooks ...AttachHooks) (err error)
	Signal(containerID string, signal syscall.Signal) (err error)
}

// PodOpts adds more information to the Pod going to be created
type PodOpts func(pod *pods.Pod) error

// AttachHooks is additional process what runs when is attached to container
type AttachHooks func(endpoint string, done <-chan struct{})

// AttachIO wraps stdin/stdout for attach
type AttachIO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// NewAttachIO is wrapper for stdin, stdout and stderr
func NewAttachIO(stdin io.Reader, stdout, stderr io.Writer) AttachIO {
	return AttachIO{stdin, stdout, stderr}
}