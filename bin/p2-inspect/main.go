package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/square/p2/Godeps/_workspace/src/gopkg.in/alecthomas/kingpin.v2"

	"github.com/square/p2/pkg/health/checker"
	"github.com/square/p2/pkg/inspect"
	"github.com/square/p2/pkg/kp"
	"github.com/square/p2/pkg/kp/consulutil"
	"github.com/square/p2/pkg/kp/flags"
	"github.com/square/p2/pkg/version"
)

var (
	filterNodeName = kingpin.Flag("node", "The node to inspect. By default, all nodes are shown.").String()
	filterPodId    = kingpin.Flag("pod", "The pod manifest ID to inspect. By default, all pods are shown.").String()
	format         = kingpin.Flag("format", "Display format").Default("tree").Enum("tree", "list")
)

func main() {
	kingpin.Version(version.VERSION)
	_, opts := flags.ParseWithConsulOptions()
	client := kp.NewConsulClient(opts)
	store := kp.NewConsulStore(client)

	intents, _, err := store.AllPods(kp.INTENT_TREE)
	if err != nil {
		message := "Could not list intent kvpairs: %s"
		if kvErr, ok := err.(consulutil.KVError); ok {
			log.Fatalf(message, kvErr.UnsafeError)
		} else {
			log.Fatalf(message, err)
		}
	}
	realities, _, err := store.AllPods(kp.REALITY_TREE)
	if err != nil {
		message := "Could not list reality kvpairs: %s"
		if kvErr, ok := err.(consulutil.KVError); ok {
			log.Fatalf(message, kvErr.UnsafeError)
		} else {
			log.Fatalf(message, err)
		}
	}

	statusMap := make(map[string]map[string]inspect.NodePodStatus)

	for _, kvp := range intents {
		if inspect.AddKVPToMap(kvp, inspect.INTENT_SOURCE, *filterNodeName, *filterPodId, statusMap) != nil {
			log.Fatal(err)
		}
	}

	for _, kvp := range realities {
		if inspect.AddKVPToMap(kvp, inspect.REALITY_SOURCE, *filterNodeName, *filterPodId, statusMap) != nil {
			log.Fatal(err)
		}
	}

	hchecker := checker.NewConsulHealthChecker(client)
	for podId := range statusMap {
		resultMap, err := hchecker.Service(podId)
		if err != nil {
			log.Fatalf("Could not retrieve health checks for pod %s: %s", podId, err)
		}

		for node, result := range resultMap {
			if *filterNodeName != "" && node != *filterNodeName {
				continue
			}

			old := statusMap[podId][node]
			old.Health = result.Status
			statusMap[podId][node] = old
		}
	}

	// Keep this switch in sync with the enum options for the "format" flag. Rethink this
	// design once there are many different formats.
	switch *format {
	case "tree":
		// Native data format is already a "tree"
		enc := json.NewEncoder(os.Stdout)
		enc.Encode(statusMap)
	case "list":
		// "List" format is a flattened version of "tree"
		output := make([]inspect.NodePodStatus, 0)
		for podId, nodes := range statusMap {
			for node, status := range nodes {
				status.PodId = podId
				status.NodeName = node
				output = append(output, status)
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.Encode(output)
	default:
		log.Fatalf("unrecognized format: %s", *format)
	}

}
