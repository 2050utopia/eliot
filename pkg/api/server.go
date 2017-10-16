package api

import (
	"fmt"
	"net"
	"syscall"
	"time"

	"golang.org/x/net/context"

	"github.com/ernoaapa/can/pkg/api/mapping"
	containers "github.com/ernoaapa/can/pkg/api/services/containers/v1"
	pods "github.com/ernoaapa/can/pkg/api/services/pods/v1"
	"github.com/ernoaapa/can/pkg/api/stream"
	"github.com/ernoaapa/can/pkg/progress"
	"github.com/ernoaapa/can/pkg/runtime"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// Server implements the GRPC API for the can-cli
type Server struct {
	client runtime.Client
	grpc   *grpc.Server
	listen string
}

// Create is 'pods' service Create implementation
func (s *Server) Create(req *pods.CreatePodRequest, server pods.Pods_CreateServer) error {
	pod := mapping.MapPodToInternalModel(req.Pod)
	var (
		done       = make(chan struct{})
		progresses = []*progress.ImageFetch{}
	)
	defer close(done)

	go func() {
		for {
			select {
			case <-done:
				// Send last update
				images := mapping.MapImageFetchProgressToAPIModel(progresses)

				if err := server.Send(&pods.CreatePodStreamResponse{Images: images}); err != nil {
					log.Warnf("Error while sending create pod status back to client: %s", err)
				}
				return // End update loop
			case <-time.After(100 * time.Millisecond):
				images := mapping.MapImageFetchProgressToAPIModel(progresses)

				if err := server.Send(&pods.CreatePodStreamResponse{Images: images}); err != nil {
					log.Warnf("Error while sending create pod status back to client: %s", err)
				}
			}
		}
	}()

	for _, container := range pod.Spec.Containers {
		progress := progress.NewImageFetch(container.Name, container.Image)
		progresses = append(progresses, progress)

		if err := s.client.PullImage(pod.Metadata.Namespace, container.Image, progress); err != nil {
			return errors.Wrapf(err, "Failed to pull image [%s]", container.Image)
		}
		progress.AllDone()

		if err := s.client.CreateContainer(pod, container); err != nil {
			return errors.Wrapf(err, "Failed to create container [%s]", container.Name)
		}
		log.Debugf("Container [%s] created", container.Name)
	}

	return nil
}

// Start is 'pods' service Start implementation
func (s *Server) Start(context context.Context, req *pods.StartPodRequest) (*pods.StartPodResponse, error) {
	containers, err := s.client.GetContainers(req.Namespace, req.Name)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to find containers to start for pod [%s] in namespace [%s]", req.Name, req.Namespace)
	}

	for _, container := range containers {
		log.Debugf("Container [%s] created, will start it", container.Name)
		if err := s.client.StartContainer(req.Namespace, container.Name, container.Tty); err != nil {
			return nil, errors.Wrapf(err, "Failed to start container [%s]", container.Name)
		}
		log.Debugf("Container [%s] started", container.Name)
	}

	return &pods.StartPodResponse{
		Pod: mapping.CreatePodAPIModel(req.Namespace, req.Name, containers),
	}, nil
}

// Delete is 'pods' service Delete implementation
func (s *Server) Delete(context context.Context, req *pods.DeletePodRequest) (*pods.DeletePodResponse, error) {
	containers, err := s.client.GetContainers(req.Namespace, req.Name)
	if err != nil {
		return nil, errors.Wrapf(err, "Cannot fetch pod containers, cannot delete pod [%s]", req.Name)
	}

	for _, container := range containers {
		err := s.client.StopContainer(req.Namespace, container.Name)
		if err != nil {
			return nil, errors.Wrapf(err, "Error while stopping container [%s]", container.Name)
		}
	}
	return &pods.DeletePodResponse{
		Pod: mapping.CreatePodAPIModel(req.Namespace, req.Name, containers),
	}, nil
}

// List is 'pods' service List implementation
func (s *Server) List(context context.Context, req *pods.ListPodsRequest) (*pods.ListPodsResponse, error) {
	p, err := s.client.GetPods(req.Namespace)
	if err != nil {
		return nil, err
	}
	return &pods.ListPodsResponse{
		Pods: mapping.MapPodsToAPIModel(p),
	}, nil
}

// Attach connects to process in container and streams stdout and stderr outputs to client
func (s *Server) Attach(server containers.Containers_AttachServer) error {
	md, ok := metadata.FromIncomingContext(server.Context())
	if !ok {
		return fmt.Errorf("Incoming attach request don't have metadata. You must provide 'Namespace' and 'ContainerID' through metadata")
	}
	log.Debugf("Received metadata: %s", md)
	var (
		namespace   = getMetadataValue(md, "namespace")
		containerID = getMetadataValue(md, "container")
	)

	if namespace == "" {
		return fmt.Errorf("You must define 'namespace' metadata")
	}

	if containerID == "" {
		return fmt.Errorf("You must define 'container' metadata")
	}

	log.Debugf("Get logs for container [%s] in namespace [%s]", containerID, namespace)
	return s.client.Attach(
		namespace, containerID,
		runtime.AttachIO{
			Stdin:  stream.NewReader(server),
			Stdout: stream.NewWriter(server, false),
			Stderr: stream.NewWriter(server, true),
		},
	)
}

// Signal connects to process in container and send signal to the process
func (s *Server) Signal(cxt context.Context, req *containers.SignalRequest) (*containers.SignalResponse, error) {
	err := s.client.Signal(req.Namespace, req.ContainerID, syscall.Signal(req.Signal))
	if err != nil {
		return nil, err
	}
	return &containers.SignalResponse{}, nil
}

func getMetadataValue(md metadata.MD, key string) string {
	if val, ok := md[key]; ok {
		return val[0]
	}
	return ""
}

// NewServer creates new API server
func NewServer(listen string, client runtime.Client) *Server {
	apiserver := &Server{
		client: client,
		listen: listen,
	}

	apiserver.grpc = grpc.NewServer()
	pods.RegisterPodsServer(apiserver.grpc, apiserver)
	containers.RegisterContainersServer(apiserver.grpc, apiserver)

	return apiserver
}

// Serve starts the server to serve GRPC server
func (s *Server) Serve() error {
	lis, err := net.Listen("tcp", s.listen)
	if err != nil {
		return errors.Wrapf(err, "Failed to start API server to listen [%s]", s.listen)
	}
	return s.grpc.Serve(lis)
}
