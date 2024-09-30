package types

import (
	clientTypes "k8s.io/apimachinery/pkg/types"
)

// ADD THE ATTRIBUTES OF YOUR CHOICE HERE
// FEW MANDATORY ATTRIBUTES ARE ADDED BY DEFAULT

// ExperimentDetails is for collecting all the experiment-related details
type ExperimentDetails struct {
	ExperimentName            string
	EngineName                string
	ChaosDuration             int
	ChaosInterval             int
	RampTime                  int
	AppNS                     string
	AppLabel                  string
	AppKind                   string
	AuxiliaryAppInfo          string
	ChaosUID                  clientTypes.UID
	InstanceID                string
	ChaosNamespace            string
	ChaosPodName              string
	Timeout                   int
	Delay                     int
	TargetContainer           string
	ChaosInjectCmd            string
	ChaosKillCmd              string
	PodsAffectedPerc          int
	TargetPods                string
	LIBImagePullPolicy        string
	IsTargetContainerProvided bool
	VMInstanceName            string
	Regions                   string
	Sequence                  string
	AuthURL                   string
	Username                  string
	Password                  string
	ProjectID                 string
	DomainName                string
}