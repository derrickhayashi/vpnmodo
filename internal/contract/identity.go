package contract

const (
	ManagerLabel = "io.vpnmodo.managed-by"
	ProfileLabel = "io.vpnmodo.profile"
	ManagerValue = "vpnmodo"
)

type DeploymentIdentity struct {
	Project string
	Labels  map[string]string
}

func Identity(name string) (DeploymentIdentity, error) {
	if !ValidName(name) {
		return DeploymentIdentity{}, ErrProfile
	}
	return DeploymentIdentity{Project: "vpnmodo-" + name, Labels: map[string]string{ManagerLabel: ManagerValue, ProfileLabel: name}}, nil
}

// Matches is an ownership filter, not an authorization boundary. Never infer
// managed ownership from a project name alone or adopt legacy containers here.
func (d DeploymentIdentity) Matches(project string, labels map[string]string) bool {
	name := d.Labels[ProfileLabel]
	return ValidName(name) && d.Labels[ManagerLabel] == ManagerValue && d.Project == "vpnmodo-"+name && project == d.Project && labels[ManagerLabel] == ManagerValue && labels[ProfileLabel] == name
}
