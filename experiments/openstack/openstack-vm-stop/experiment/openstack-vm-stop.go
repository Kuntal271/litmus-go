package experiment

import (
	"os"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/litmuschaos/chaos-operator/api/litmuschaos/v1alpha1"
	litmusLIB "github.com/litmuschaos/litmus-go/chaoslib/litmus/openstack-vm-stop/lib"
	"github.com/litmuschaos/litmus-go/pkg/clients"
	"github.com/litmuschaos/litmus-go/pkg/events"
	"github.com/litmuschaos/litmus-go/pkg/log"
	experimentEnv "github.com/litmuschaos/litmus-go/pkg/openstack/openstack-vm-stop/environment"
	experimentTypes "github.com/litmuschaos/litmus-go/pkg/openstack/openstack-vm-stop/types"
	"github.com/litmuschaos/litmus-go/pkg/probe"
	"github.com/litmuschaos/litmus-go/pkg/result"
	"github.com/litmuschaos/litmus-go/pkg/types"
	"github.com/litmuschaos/litmus-go/pkg/utils/common"
	"github.com/sirupsen/logrus"
)

// VMInstanceStop executes the experiment steps by injecting chaos into the specified VM instances
func VMInstanceStop(clients clients.ClientSets) {

	var (
		err error
	)

	experimentsDetails := experimentTypes.ExperimentDetails{}
	resultDetails := types.ResultDetails{}
	eventsDetails := types.EventDetails{}
	chaosDetails := types.ChaosDetails{}

	// Fetching all the ENV passed from the runner pod
	experimentEnv.GetENV(&experimentsDetails)
	log.Infof("[PreReq]: Procured the ENV for the %v experiment", os.Getenv("EXPERIMENT_NAME"))

	// Initialize the chaos attributes
	types.InitialiseChaosVariables(&chaosDetails)

	// Initialize Chaos Result Parameters
	types.SetResultAttributes(&resultDetails, chaosDetails)

	if experimentsDetails.EngineName != "" {
		// Get values from chaosengine. Bail out upon error, as we haven't entered exp business logic yet
		if err := types.GetValuesFromChaosEngine(&chaosDetails, clients, &resultDetails); err != nil {
			log.Errorf("Unable to initialize the probes, err: %v", err)
			return
		}
	}

	// Updating the chaos result in the beginning of experiment
	log.Infof("[PreReq]: Updating the chaos result of %v experiment (SOT)", experimentsDetails.ExperimentName)
	if err := result.ChaosResult(&chaosDetails, clients, &resultDetails, "SOT"); err != nil {
		log.Errorf("Unable to Create the Chaos Result, err: %v", err)
		result.RecordAfterFailure(&chaosDetails, &resultDetails, err, clients, &eventsDetails)
		return
	}

	// Set the chaos result uid
	result.SetResultUID(&resultDetails, clients, &chaosDetails)

	// generating the event in chaosresult to mark the verdict as awaited
	msg := "experiment: " + experimentsDetails.ExperimentName + ", Result: Awaited"
	types.SetResultEventAttributes(&eventsDetails, types.AwaitedVerdict, msg, "Normal", &resultDetails)
	events.GenerateEvents(&eventsDetails, clients, &chaosDetails, "ChaosResult")

	// Calling AbortWatcher go routine
	go common.AbortWatcherWithoutExit(experimentsDetails.ExperimentName, clients, &resultDetails, &chaosDetails, &eventsDetails)

	// DISPLAY THE INSTANCE INFORMATION
	log.InfoWithValues("The VM instance information is as follows", logrus.Fields{
		"Instance Names": experimentsDetails.VMInstanceName,
		"Region":         experimentsDetails.Regions,
		"Sequence":       experimentsDetails.Sequence,
	})

	if experimentsDetails.EngineName != "" {
		// marking AUT as running
		msg := "AUT: Running"

		// run the probes in the pre-chaos check
		if len(resultDetails.ProbeDetails) != 0 {
			if err := probe.RunProbes(&chaosDetails, clients, &resultDetails, "PreChaos", &eventsDetails); err != nil {
				log.Errorf("Probe Failed, err: %v", err)
				msg = "AUT: Running, Probes: Unsuccessful"
				types.SetEngineEventAttributes(&eventsDetails, types.PreChaosCheck, msg, "Warning", &chaosDetails)
				events.GenerateEvents(&eventsDetails, clients, &chaosDetails, "ChaosEngine")
				result.RecordAfterFailure(&chaosDetails, &resultDetails, err, clients, &eventsDetails)
				return
			}
			msg = "AUT: Running, Probes: Successful"
		}
		// generating the events for the pre-chaos check
		types.SetEngineEventAttributes(&eventsDetails, types.PreChaosCheck, msg, "Normal", &chaosDetails)
		events.GenerateEvents(&eventsDetails, clients, &chaosDetails, "ChaosEngine")
	}

	authOpts := gophercloud.AuthOptions{
		IdentityEndpoint: experimentsDetails.AuthURL,
		Username:         experimentsDetails.Username,
		Password:         experimentsDetails.Password,
		TenantID:         experimentsDetails.ProjectID,
		DomainName:       experimentsDetails.DomainName,
	}
	// Create a compute service to access the compute engine resources
	provider, err := openstack.AuthenticatedClient(authOpts)
	if err != nil {
		log.Errorf("Failed to obtain an OpenStack client, err: %v", err)
		result.RecordAfterFailure(&chaosDetails, &resultDetails, err, clients, &eventsDetails)
		return
	}
	computeService, err := openstack.NewComputeV2(provider, gophercloud.EndpointOpts{})
	if err != nil {
		log.Errorf("Failed to obtain a compute service, err: %v", err)
		result.RecordAfterFailure(&chaosDetails, &resultDetails, err, clients, &eventsDetails)
		return
	}

	// Verify that the OpenStack VM instance(s) is in ACTIVE state (pre-chaos)
	if chaosDetails.DefaultHealthCheck {
		if err := servers.WaitForStatus(computeService, experimentsDetails.VMInstanceName, "ACTIVE", 0); err != nil {
			log.Errorf("Failed to get the VM instance status, err: %v", err)
			result.RecordAfterFailure(&chaosDetails, &resultDetails, err, clients, &eventsDetails)
			return
		}

		log.Info("[Status]: VM instance is in active state (pre-chaos)")
	}

	chaosDetails.Phase = types.ChaosInjectPhase

	if err := litmusLIB.PrepareVMStop(computeService, &experimentsDetails, clients, &resultDetails, &eventsDetails, &chaosDetails); err != nil {
		log.Errorf("Chaos injection failed, err: %v", err)
		result.RecordAfterFailure(&chaosDetails, &resultDetails, err, clients, &eventsDetails)
		return
	}

	log.Infof("[Confirmation]: %v chaos has been injected successfully", experimentsDetails.ExperimentName)
	resultDetails.Verdict = v1alpha1.ResultVerdictPassed

	chaosDetails.Phase = types.PostChaosPhase

	// Verify the OpenStack VM instance is in ACTIVE status (post-chaos)
	if chaosDetails.DefaultHealthCheck {
		if err := servers.WaitForStatus(computeService, experimentsDetails.VMInstanceName, "ACTIVE", 0); err != nil {
			log.Errorf("Failed to get the VM instance status, err: %v", err)
			result.RecordAfterFailure(&chaosDetails, &resultDetails, err, clients, &eventsDetails)
			return
		}

		log.Info("[Status]: VM instance is in active state (post-chaos)")
	}

	if experimentsDetails.EngineName != "" {
		// marking AUT as running
		msg := "AUT: Running"

		// run the probes in the post-chaos check
		if len(resultDetails.ProbeDetails) != 0 {
			if err := probe.RunProbes(&chaosDetails, clients, &resultDetails, "PostChaos", &eventsDetails); err != nil {
				log.Errorf("Probes Failed, err: %v", err)
				msg = "AUT: Running, Probes: Unsuccessful"
				types.SetEngineEventAttributes(&eventsDetails, types.PostChaosCheck, msg, "Warning", &chaosDetails)
				events.GenerateEvents(&eventsDetails, clients, &chaosDetails, "ChaosEngine")
				result.RecordAfterFailure(&chaosDetails, &resultDetails, err, clients, &eventsDetails)
				return
			}
			msg = "AUT: Running, Probes: Successful"
		}

		// generating post chaos event
		types.SetEngineEventAttributes(&eventsDetails, types.PostChaosCheck, msg, "Normal", &chaosDetails)
		events.GenerateEvents(&eventsDetails, clients, &chaosDetails, "ChaosEngine")
	}

	// Updating the chaosResult in the end of experiment
	log.Infof("[The End]: Updating the chaos result of %v experiment (EOT)", experimentsDetails.ExperimentName)
	if err := result.ChaosResult(&chaosDetails, clients, &resultDetails, "EOT"); err != nil {
		log.Errorf("Unable to Update the Chaos Result, err:  %v", err)
		return
	}

	// generating the event in chaosresult to mark the verdict as pass/fail
	msg = "experiment: " + experimentsDetails.ExperimentName + ", Result: " + string(resultDetails.Verdict)
	reason, eventType := types.GetChaosResultVerdictEvent(resultDetails.Verdict)
	types.SetResultEventAttributes(&eventsDetails, reason, msg, eventType, &resultDetails)
	events.GenerateEvents(&eventsDetails, clients, &chaosDetails, "ChaosResult")

	if experimentsDetails.EngineName != "" {
		msg := experimentsDetails.ExperimentName + " experiment has been " + string(resultDetails.Verdict) + "ed"
		types.SetEngineEventAttributes(&eventsDetails, types.Summary, msg, "Normal", &chaosDetails)
		events.GenerateEvents(&eventsDetails, clients, &chaosDetails, "ChaosEngine")
	}
}