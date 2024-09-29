package lib

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/startstop"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"

	"github.com/litmuschaos/litmus-go/pkg/cerrors"
	"github.com/palantir/stacktrace"

	clients "github.com/litmuschaos/litmus-go/pkg/clients"
	"github.com/litmuschaos/litmus-go/pkg/log"
	experimentTypes "github.com/litmuschaos/litmus-go/pkg/openstack/openstack-vm-stop/types"
	"github.com/litmuschaos/litmus-go/pkg/types"
	"github.com/litmuschaos/litmus-go/pkg/utils/common"
)

var (
	abort chan os.Signal
)

func abortWatcher(computeService *gophercloud.ServiceClient, experimentsDetails *experimentTypes.ExperimentDetails, instanceNamesList []string, chaosDetails *types.ChaosDetails) {
	<-abort

	log.Info("[Abort]: Chaos Revert Started")

	for i := range instanceNamesList {
		instanceID := instanceNamesList[i]

		// Get the current VM status
		instance, err := servers.Get(computeService, instanceID).Extract()
		if err != nil {
			log.Errorf("Failed to get status for VM instance %s when abort signal is received: %v", instanceID, err)
			continue
		}

		// If the instance is not running, revert the chaos (start the instance)
		if instance.Status != "ACTIVE" {
			log.Infof("[Abort]: Waiting for VM instance %s to shut down", instanceID)

			// Wait for the VM instance to stop
			if err := waitForInstanceStatus(computeService, instanceID, "SHUTOFF", experimentsDetails.Timeout, experimentsDetails.Delay); err != nil {
				log.Errorf("Unable to wait for shutdown of instance %s: %v", instanceID, err)
				continue
			}

			log.Infof("[Abort]: Starting VM instance %s as abort signal is received", instanceID)
			if err := startInstance(computeService, instanceID); err != nil {
				log.Errorf("Failed to start VM instance %s when abort signal is received: %v", instanceID, err)
				continue
			}
		}

		// Mark the instance as reverted
		common.SetTargets(instanceID, "reverted", "VM", chaosDetails)
	}

	log.Info("[Abort]: Chaos Revert Completed")
	os.Exit(1)
}

// waitForInstanceStatus waits for an instance to reach the desired status
func waitForInstanceStatus(computeService *gophercloud.ServiceClient, instanceID, status string, timeout, delay int) error {
	log.Infof("Waiting for instance %s to reach status %s", instanceID, status)

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)

	for time.Now().Before(deadline) {
		instance, err := servers.Get(computeService, instanceID).Extract()
		if err != nil {
			return fmt.Errorf("failed to get instance status: %v", err)
		}

		if instance.Status == status {
			log.Infof("Instance %s has reached status %s", instanceID, status)
			return nil
		}

		time.Sleep(time.Duration(delay) * time.Second)
	}

	return fmt.Errorf("timeout waiting for instance %s to reach status %s", instanceID, status)
}

// PrepareVMStop contains the preparation and injection steps for the experiment
func PrepareVMStop(computeService *gophercloud.ServiceClient, experimentsDetails *experimentTypes.ExperimentDetails, clients clients.ClientSets, resultDetails *types.ResultDetails, eventsDetails *types.EventDetails, chaosDetails *types.ChaosDetails) error {

	// inject channel is used to transmit signal notifications.
	inject := make(chan os.Signal, 1)
	// catch and relay certain signal(s) to inject channel.
	signal.Notify(inject, os.Interrupt, syscall.SIGTERM)

	// abort channel is used to transmit signal notifications.
	abort := make(chan os.Signal, 1)
	// catch and relay certain signal(s) to abort channel.
	signal.Notify(abort, os.Interrupt, syscall.SIGTERM)

	// waiting for the ramp time before chaos injection
	if experimentsDetails.RampTime != 0 {
		log.Infof("[Ramp]: Waiting for the %vs ramp time before injecting chaos", experimentsDetails.RampTime)
		common.WaitForDuration(experimentsDetails.RampTime)
	}

	// get the instance name or list of instance names
	instanceIDList := strings.Split(experimentsDetails.InstanceID, ",")

	go abortWatcher(computeService, experimentsDetails, instanceIDList, chaosDetails)

	switch strings.ToLower(experimentsDetails.Sequence) {
	case "serial":
		if err := injectChaosInSerialMode(computeService, experimentsDetails, instanceIDList, clients, resultDetails, eventsDetails, chaosDetails); err != nil {
			return stacktrace.Propagate(err, "could not run chaos in serial mode")
		}
	case "parallel":
		if err := injectChaosInParallelMode(computeService, experimentsDetails, instanceIDList, clients, resultDetails, eventsDetails, chaosDetails); err != nil {
			return stacktrace.Propagate(err, "could not run chaos in parallel mode")
		}
	default:
		return cerrors.Error{ErrorCode: cerrors.ErrorTypeGeneric, Reason: fmt.Sprintf("'%s' sequence is not supported", experimentsDetails.Sequence)}
	}

	// wait for the ramp time after chaos injection
	if experimentsDetails.RampTime != 0 {
		log.Infof("[Ramp]: Waiting for the %vs ramp time after injecting chaos", experimentsDetails.RampTime)
		common.WaitForDuration(experimentsDetails.RampTime)
	}

	return nil
}

// injectChaosInSerialMode stops and starts instances serially
func injectChaosInSerialMode(computeService *gophercloud.ServiceClient, experimentsDetails *experimentTypes.ExperimentDetails, instanceNamesList []string, clients clients.ClientSets, resultDetails *types.ResultDetails, eventsDetails *types.EventDetails, chaosDetails *types.ChaosDetails) error {
	for _, instanceName := range instanceNamesList {
		log.Infof("[Chaos]: Stopping OpenStack instance: %s", instanceName)
		if err := stopInstance(computeService, instanceName); err != nil {
			return stacktrace.Propagate(err, "failed to stop instance %s", instanceName)
		}

		// Wait for the instance to stop
		if err := waitForInstanceStatus(computeService, instanceName, "SHUTOFF", 0, 0); err != nil {
			return stacktrace.Propagate(err, "instance %s did not reach SHUTOFF state", instanceName)
		}

		log.Infof("[Chaos]: Starting OpenStack instance: %s", instanceName)
		if err := startInstance(computeService, instanceName); err != nil {
			return stacktrace.Propagate(err, "failed to start instance %s", instanceName)
		}

		// Wait for the instance to return to ACTIVE state
		if err := waitForInstanceStatus(computeService, instanceName, "ACTIVE", 0, 0); err != nil {
			return stacktrace.Propagate(err, "instance %s did not return to ACTIVE state", instanceName)
		}
	}
	return nil
}

// injectChaosInParallelMode stops and starts instances in parallel
func injectChaosInParallelMode(computeService *gophercloud.ServiceClient, experimentsDetails *experimentTypes.ExperimentDetails, instanceNamesList []string, clients clients.ClientSets, resultDetails *types.ResultDetails, eventsDetails *types.EventDetails, chaosDetails *types.ChaosDetails) error {
	errChan := make(chan error, len(instanceNamesList))
	for _, instanceName := range instanceNamesList {
		go func(instanceName string) {
			log.Infof("[Chaos]: Stopping OpenStack instance: %s", instanceName)
			if err := stopInstance(computeService, instanceName); err != nil {
				errChan <- stacktrace.Propagate(err, "failed to stop instance %s", instanceName)
				return
			}

			// Wait for the instance to stop
			if err := waitForInstanceStatus(computeService, instanceName, "SHUTOFF", 0, 0); err != nil {
				errChan <- stacktrace.Propagate(err, "instance %s did not reach SHUTOFF state", instanceName)
				return
			}

			log.Infof("[Chaos]: Starting OpenStack instance: %s", instanceName)
			if err := startInstance(computeService, instanceName); err != nil {
				errChan <- stacktrace.Propagate(err, "failed to start instance %s", instanceName)
				return
			}

			// Wait for the instance to return to ACTIVE state
			if err := waitForInstanceStatus(computeService, instanceName, "ACTIVE", 0, 0); err != nil {
				errChan <- stacktrace.Propagate(err, "instance %s did not return to ACTIVE state", instanceName)
				return
			}

			errChan <- nil
		}(instanceName)
	}

	for range instanceNamesList {
		if err := <-errChan; err != nil {
			return err
		}
	}
	return nil
}

// stopInstance stops an OpenStack VM instance
func stopInstance(computeService *gophercloud.ServiceClient, instanceID string) error {
	return startstop.Stop(computeService, instanceID).ExtractErr()
}

// startInstance starts an OpenStack VM instance
func startInstance(computeService *gophercloud.ServiceClient, instanceID string) error {
	return startstop.Start(computeService, instanceID).ExtractErr()
}
