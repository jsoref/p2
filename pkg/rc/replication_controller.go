package rc

import (
	"fmt"
	"strings"
	"time"

	klabels "github.com/square/p2/Godeps/_workspace/src/k8s.io/kubernetes/pkg/labels"
	"github.com/square/p2/Godeps/_workspace/src/k8s.io/kubernetes/pkg/util/sets"

	"github.com/square/p2/pkg/kp"
	"github.com/square/p2/pkg/kp/rcstore"
	"github.com/square/p2/pkg/labels"
	"github.com/square/p2/pkg/logging"
	"github.com/square/p2/pkg/pods"
	"github.com/square/p2/pkg/rc/fields"
	"github.com/square/p2/pkg/types"
	"github.com/square/p2/pkg/util"
)

const (
	// This label is applied to pods owned by an RC.
	RCIDLabel = "replication_controller_id"
)

type ReplicationController interface {
	ID() fields.ID

	// WatchDesires causes the replication controller to watch for any changes to its desired state.
	// It is expected that a replication controller is aware of a backing rcstore against which to perform this watch.
	// Upon seeing any changes, the replication controller schedules or unschedules pods to meet the desired state.
	// This spawns a goroutine that performs the watch and returns a channel on which errors are sent.
	// The caller must consume from the error channel.
	// Failure to do so blocks the replication controller from meeting desires.
	// Send a struct{} on the quit channel to stop the goroutine.
	// The error channel will be closed in response.
	WatchDesires(quit <-chan struct{}) <-chan error

	// CurrentPods() returns all pods managed by this replication controller.
	CurrentPods() (PodLocations, error)
}

type PodLocation struct{ Node, PodID string }
type PodLocations []PodLocation

// Nodes returns a list of just the locations' nodes.
func (l PodLocations) Nodes() []string {
	nodes := make([]string, len(l))
	for i, pod := range l {
		nodes[i] = pod.Node
	}
	return nodes
}

// These methods are the same as the methods of the same name in kp.Store.
// Replication controllers have no need of any methods other than these.
type kpStore interface {
	SetPod(
		podPrefix kp.PodPrefix,
		nodeName string,
		manifest pods.Manifest,
	) (time.Duration, error)

	Pod(
		podPrefix kp.PodPrefix,
		nodeName string,
		podId types.PodID,
	) (pods.Manifest, time.Duration, error)

	DeletePod(podPrefix kp.PodPrefix,
		nodeName string,
		manifestID types.PodID,
	) (time.Duration, error)
}

type replicationController struct {
	fields.RC

	logger logging.Logger

	kpStore       kpStore
	rcStore       rcstore.Store
	scheduler     Scheduler
	podApplicator labels.Applicator
}

func New(
	fields fields.RC,
	kpStore kpStore,
	rcStore rcstore.Store,
	scheduler Scheduler,
	podApplicator labels.Applicator,
	logger logging.Logger,
) ReplicationController {
	return &replicationController{
		RC: fields,

		logger:        logger,
		kpStore:       kpStore,
		rcStore:       rcStore,
		scheduler:     scheduler,
		podApplicator: podApplicator,
	}
}

func (rc *replicationController) ID() fields.ID {
	return rc.RC.ID
}

func (rc *replicationController) WatchDesires(quit <-chan struct{}) <-chan error {
	desiresChanged, errInChannel := rc.rcStore.Watch(&rc.RC, quit)

	errOutChannel := make(chan error)
	channelsClosed := make(chan struct{})

	// When seeing any changes, try to meet them.
	// If meeting produces any error, send it on the output error channel.
	go func() {
		for range desiresChanged {
			err := rc.meetDesires()
			if err != nil {
				errOutChannel <- err
			}
		}
		channelsClosed <- struct{}{}
	}()

	// When seeing any errors, forward them to the output error channel.
	// (Notice that two goroutines may be writing to the output error channel)
	go func() {
		for err := range errInChannel {
			errOutChannel <- err
		}
		channelsClosed <- struct{}{}
	}()

	// If both channels I'm watching are closed,
	// which would happen when my caller requests a quit
	// (the quit gets forwarded to the rc watcher)
	// close my output channel.
	go func() {
		<-channelsClosed
		<-channelsClosed
		close(channelsClosed)
		close(errOutChannel)
	}()

	return errOutChannel
}

func (rc *replicationController) meetDesires() error {
	rc.logger.NoFields().Infof("Meeting with desired replicas %d, disabled %v", rc.ReplicasDesired, rc.Disabled)

	// If we're disabled, we do nothing, nor is it an error
	// (it's a normal possibility to be disabled)
	if rc.Disabled {
		return nil
	}

	current, err := rc.CurrentPods()
	if err != nil {
		return err
	}

	rc.logger.NoFields().Infof("Currently on nodes %s", current)

	nodesChanged := false
	if rc.ReplicasDesired > len(current) {
		err := rc.addPods(current)
		if err != nil {
			return err
		}
		nodesChanged = true
	} else if len(current) > rc.ReplicasDesired {
		err := rc.removePods(current)
		if err != nil {
			return err
		}
		nodesChanged = true
	} else {
		rc.logger.NoFields().Debugln("Taking no action")
	}

	if nodesChanged {
		current, err = rc.CurrentPods()
		if err != nil {
			return err
		}
	}

	return rc.ensureConsistency(current)
}

func (rc *replicationController) addPods(current PodLocations) error {
	currentNodes := current.Nodes()
	eligible, err := rc.eligibleNodes()
	if err != nil {
		return err
	}

	// TODO: With Docker or runc we would not be constrained to running only once per node.
	// So it may be the case that we need to make the Scheduler interface smarter and use it here.
	possible := sets.NewString(eligible...).Difference(sets.NewString(currentNodes...))

	// Users want deterministic ordering of nodes being populated to a new
	// RC. Move nodes in sorted order by hostname to achieve this
	possibleSorted := possible.List()
	toSchedule := rc.ReplicasDesired - len(currentNodes)

	rc.logger.NoFields().Infof("Need to schedule %d nodes out of %s", toSchedule, possible)

	for i := 0; i < toSchedule; i++ {
		if len(possibleSorted) < i+1 {
			return util.Errorf(
				"Not enough nodes to meet desire: %d replicas desired, %d currentNodes, %d eligible. Scheduled on %d nodes instead.",
				rc.ReplicasDesired, len(currentNodes), len(eligible), i,
			)
		}
		scheduleOn := possibleSorted[i]

		err := rc.schedule(scheduleOn)
		if err != nil {
			return err
		}
	}
	return nil
}

func (rc *replicationController) removePods(current PodLocations) error {
	currentNodes := current.Nodes()
	eligible, err := rc.eligibleNodes()
	if err != nil {
		return err
	}

	// If we need to downsize the number of nodes, prefer any in current that are not eligible anymore.
	// TODO: evaluate changes to 'eligible' more frequently
	preferred := sets.NewString(currentNodes...).Difference(sets.NewString(eligible...))
	rest := sets.NewString(currentNodes...).Difference(preferred)
	toUnschedule := len(current) - rc.ReplicasDesired
	rc.logger.NoFields().Infof("Need to unschedule %d nodes out of %s", toUnschedule, current)

	for i := 0; i < toUnschedule; i++ {
		unscheduleFrom, ok := preferred.PopAny()
		if !ok {
			var ok bool
			unscheduleFrom, ok = rest.PopAny()
			if !ok {
				// This should be mathematically impossible unless replicasDesired was negative
				return util.Errorf(
					"Unable to unschedule enough nodes to meet replicas desired: %d replicas desired, %d current.",
					rc.ReplicasDesired, len(current),
				)
			}
		}
		err := rc.unschedule(unscheduleFrom)
		if err != nil {
			return err
		}
	}
	return nil
}

func (rc *replicationController) ensureConsistency(current PodLocations) error {
	manifestSHA, err := rc.Manifest.SHA()
	if err != nil {
		return err
	}
	for _, pod := range current {
		intent, _, err := rc.kpStore.Pod(kp.INTENT_TREE, pod.Node, types.PodID(pod.PodID))
		if err != nil && err != pods.NoCurrentManifest {
			return err
		}
		var intentSHA string
		if intent != nil {
			intentSHA, err = intent.SHA()
			if err != nil {
				rc.logger.WithError(err).WithField("node", pod.Node).Warn("Could not hash manifest to determine consistency of intent")
			}
			if intentSHA == manifestSHA {
				continue
			}
		}

		rc.logger.WithField("node", pod.Node).WithField("intentManifestSHA", intentSHA).Info("Found inconsistency in scheduled manifest")
		rc.schedule(pod.Node)
	}

	return nil
}

func (rc *replicationController) eligibleNodes() ([]string, error) {
	return rc.scheduler.EligibleNodes(rc.Manifest, rc.NodeSelector)
}

func (rc *replicationController) CurrentPods() (PodLocations, error) {
	selector := klabels.Everything().Add(RCIDLabel, klabels.EqualsOperator, []string{rc.ID().String()})

	podMatches, err := rc.podApplicator.GetMatches(selector, labels.POD)
	if err != nil {
		return nil, err
	}

	result := make(PodLocations, len(podMatches))
	for i, node := range podMatches {
		// ID will be something like <nodename>/<podid>.
		parts := strings.SplitN(node.ID, "/", 2)
		if len(parts) < 2 {
			return nil, fmt.Errorf("malformed pod label %s", node.ID)
		}
		result[i].Node = parts[0]
		result[i].PodID = parts[1]
	}
	return result, nil
}

// forEachLabel Attempts to apply the supplied function to all user-supplied labels
// and the reserved labels.
// If forEachLabel encounters any error applying the function, it returns that error immediately.
// The function is not further applied to subsequent labels on an error.
func (rc *replicationController) forEachLabel(node string, f func(id, k, v string) error) error {
	id := node + "/" + string(rc.Manifest.ID())

	// user-requested labels.
	for k, v := range rc.PodLabels {
		if err := f(id, k, v); err != nil {
			return err
		}
	}
	// our reserved labels.
	return f(id, RCIDLabel, rc.ID().String())
}

func (rc *replicationController) schedule(node string) error {
	rc.logger.NoFields().Infof("Scheduling on %s", node)
	err := rc.forEachLabel(node, func(podID, k, v string) error {
		return rc.podApplicator.SetLabel(labels.POD, podID, k, v)
	})
	if err != nil {
		return err
	}

	_, err = rc.kpStore.SetPod(kp.INTENT_TREE, node, rc.Manifest)
	return err
}

func (rc *replicationController) unschedule(node string) error {
	rc.logger.NoFields().Infof("Uncheduling from %s", node)
	_, err := rc.kpStore.DeletePod(kp.INTENT_TREE, node, rc.Manifest.ID())
	if err != nil {
		return err
	}

	return rc.forEachLabel(node, func(podID, k, _ string) error {
		return rc.podApplicator.RemoveLabel(labels.POD, podID, k)
	})
}
