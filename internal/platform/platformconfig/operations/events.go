package operations

import (
	"encoding/json"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

const (
	PropertySetType   = "platform:admin:platform-config:property-set"
	AccessGrantedType = "platform:admin:platform-config:access-granted"
	AccessRevokedType = "platform:admin:platform-config:access-revoked"
	Source            = "platform:admin"
)

func subjectFor(id string) string { return "platform.platformconfig." + id }
func groupFor(id string) string   { return "platform:platformconfig:" + id }

type PropertySet struct {
	Metadata        usecase.EventMetadata
	ConfigID        string
	ApplicationCode string
	Section         string
	Property        string
}

func (e PropertySet) EventID() string       { return e.Metadata.EventID }
func (e PropertySet) EventType() string     { return PropertySetType }
func (e PropertySet) SpecVersion() string   { return "1.0" }
func (e PropertySet) Source() string        { return Source }
func (e PropertySet) Subject() string       { return subjectFor(e.ConfigID) }
func (e PropertySet) Time() time.Time       { return e.Metadata.OccurredAt }
func (e PropertySet) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e PropertySet) CorrelationID() string { return e.Metadata.CorrelationID }
func (e PropertySet) CausationID() string   { return e.Metadata.CausationID }
func (e PropertySet) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e PropertySet) MessageGroup() string  { return groupFor(e.ConfigID) }
func (e PropertySet) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ConfigID        string `json:"configId"`
		ApplicationCode string `json:"applicationCode"`
		Section         string `json:"section"`
		Property        string `json:"property"`
	}{e.ConfigID, e.ApplicationCode, e.Section, e.Property})
}

type AccessGranted struct {
	Metadata        usecase.EventMetadata
	AccessID        string
	ApplicationCode string
	RoleCode        string
	CanWrite        bool
}

func (e AccessGranted) EventID() string       { return e.Metadata.EventID }
func (e AccessGranted) EventType() string     { return AccessGrantedType }
func (e AccessGranted) SpecVersion() string   { return "1.0" }
func (e AccessGranted) Source() string        { return Source }
func (e AccessGranted) Subject() string       { return subjectFor(e.AccessID) }
func (e AccessGranted) Time() time.Time       { return e.Metadata.OccurredAt }
func (e AccessGranted) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e AccessGranted) CorrelationID() string { return e.Metadata.CorrelationID }
func (e AccessGranted) CausationID() string   { return e.Metadata.CausationID }
func (e AccessGranted) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e AccessGranted) MessageGroup() string  { return groupFor(e.AccessID) }
func (e AccessGranted) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		AccessID        string `json:"accessId"`
		ApplicationCode string `json:"applicationCode"`
		RoleCode        string `json:"roleCode"`
		CanWrite        bool   `json:"canWrite"`
	}{e.AccessID, e.ApplicationCode, e.RoleCode, e.CanWrite})
}

type AccessRevoked struct {
	Metadata        usecase.EventMetadata
	AccessID        string
	ApplicationCode string
	RoleCode        string
}

func (e AccessRevoked) EventID() string       { return e.Metadata.EventID }
func (e AccessRevoked) EventType() string     { return AccessRevokedType }
func (e AccessRevoked) SpecVersion() string   { return "1.0" }
func (e AccessRevoked) Source() string        { return Source }
func (e AccessRevoked) Subject() string       { return subjectFor(e.AccessID) }
func (e AccessRevoked) Time() time.Time       { return e.Metadata.OccurredAt }
func (e AccessRevoked) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e AccessRevoked) CorrelationID() string { return e.Metadata.CorrelationID }
func (e AccessRevoked) CausationID() string   { return e.Metadata.CausationID }
func (e AccessRevoked) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e AccessRevoked) MessageGroup() string  { return groupFor(e.AccessID) }
func (e AccessRevoked) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		AccessID        string `json:"accessId"`
		ApplicationCode string `json:"applicationCode"`
		RoleCode        string `json:"roleCode"`
	}{e.AccessID, e.ApplicationCode, e.RoleCode})
}
