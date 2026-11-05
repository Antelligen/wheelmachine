package api

import (
	"encoding/json"
	"time"
)

// The states a machine can be in.
const (
	StateStarting   = "starting"   // being made ready
	StateRunning    = "running"    // running
	StatePaused     = "paused"     // memory kept, nothing running
	StateStopped    = "stopped"    // files kept, memory not
	StateArchived   = "archived"   // files in storage
	StateRecovering = "recovering" // a fault on our side stopped it, and it is being brought back
	StateDeleted    = "deleted"
	StateFailed     = "failed" // it never came up, and Failure says why; nothing is charged for it, and it is gone
)

// States lists every state, in the order the API documents them.
var States = []string{StateStarting, StateRunning, StatePaused, StateStopped, StateArchived, StateRecovering, StateDeleted, StateFailed}

// ListedStates lists the states machines are listed by: every state but deleted, as a deleted machine is never listed.
// Failed lists the machines that never came up, each with why.
var ListedStates = []string{StateStarting, StateRunning, StatePaused, StateStopped, StateArchived, StateRecovering, StateFailed}

// The scopes a key can hold.
const (
	ScopeAll           = "cloud:*"
	ScopeMachinesRead  = "cloud:machines:read"
	ScopeMachinesWrite = "cloud:machines:write"
	ScopeMachinesExec  = "cloud:machines:exec"
	ScopeGPUs          = "cloud:gpus"
	ScopeProjectsWrite = "cloud:projects:write"
	ScopeKeysWrite     = "cloud:keys:write"
	ScopeSecretsWrite  = "cloud:secrets:write" // set and delete secrets, and give them to machines
	ScopeFoldersRead   = "cloud:folders:read"  // see folders and download what is in them
	ScopeFoldersWrite  = "cloud:folders:write" // change what is in folders, and make team folders
)

// Scopes lists every scope.
var Scopes = []string{ScopeAll, ScopeMachinesRead, ScopeMachinesWrite, ScopeMachinesExec, ScopeGPUs, ScopeProjectsWrite, ScopeKeysWrite,
	ScopeSecretsWrite, ScopeFoldersRead, ScopeFoldersWrite}

// Page is one page of a list. Next is where the page after it begins (give it as `after`); nil when there is none.
type Page[T any] struct {
	Object string  `json:"object"`
	Data   []T     `json:"data"`
	Next   *string `json:"next"`
}

// After is where the next page begins, "" when this is the last.
func (p *Page[T]) After() string {
	if p == nil || p.Next == nil {
		return ""
	}
	return *p.Next
}

// The kinds of account a call acts in: a person's own, or an organisation's.
const (
	AccountPerson = "person"
	AccountOrg    = "org"
)

// The roles a person has in an organisation. In their own account a person is its owner.
const (
	RoleOwner   = "owner"   // may do everything
	RoleAdmin   = "admin"   // everything but change owners
	RoleBilling = "billing" // sees what machines cost and the people, no machine, and changes nothing but a person's limit for the month
	RoleMember  = "member"  // makes and uses machines in their teams: those they hold, and in a team they lead those its members hold
)

// Me is the account and key a call is made with: the account it acts in, the person behind it - the one who made the
// key - their role there, and, in an organisation, their teams and their own limits there.
type Me struct {
	Object  string  `json:"object"`
	Account Account `json:"account"`
	Person  *Person `json:"person"`
	Role    string  `json:"role"` // owner in a person's own account
	// Teams is, in an organisation and for a member, the projects of their teams - empty for one in no team yet; nil
	// for any other role, and in a person's own account.
	Teams []string `json:"teams"`
	// Limits is the person's own limits in the organisation, beside its own; nil in their own account.
	Limits   *PersonLimits `json:"limits"`
	Scopes   []string      `json:"scopes"` // what the call holds
	Key      Key           `json:"key"`
	Standing Standing      `json:"standing"`
	// Levels is, in an organisation, the person's level in each team they manage, by its project (LevelApprover,
	// LevelManager or LevelFull); empty otherwise.
	Levels map[string]string `json:"levels,omitempty"`
}

// A team manager's levels: an approver sees the team's members' machines, watches their screens and decides the team's
// hand-offs; a team manager also starts, stops, pauses and deletes those machines and puts people in the team; a full
// manager also decides its forks, sets its rules and makes approvers. None uses or forks a member's machine.
const (
	LevelApprover = "approver"
	LevelManager  = "manager"
	LevelFull     = "full"
)

// Account is the account a call acts in.
type Account struct {
	ID   string `json:"id"`
	Kind string `json:"kind,omitempty"` // AccountPerson or AccountOrg
	Name string `json:"name,omitempty"` // an organisation's name, or the person's in their own account
}

// InOrg says whether the call acts in an organisation.
func (m *Me) InOrg() bool { return m != nil && m.Account.Kind == AccountOrg }

// Person is a person behind a call, or in an organisation, by their own account's id. Email is said with the
// dashboard's sign-in only, never to a key.
type Person struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Handle      string  `json:"handle"` // their @handle, without its @
	Email       *string `json:"email"`
	GitHubLogin *string `json:"github_login"`
}

// PersonLimits is a person's own limits in an organisation, beside the organisation's own: a nil number is no limit of
// their own.
type PersonLimits struct {
	MaxMachines *int     `json:"max_machines"`  // the machines they may hold
	MaxVCPUs    *int     `json:"max_vcpus"`     // the CPUs their machines may run at once
	GPUs        bool     `json:"gpus"`          // whether they may make GPU machines
	MonthCapUSD *float64 `json:"month_cap_usd"` // what their machines may come to in a month, in dollars
}

// Org is an organisation as one of its people sees it: their role in it, and whether they are its primary owner, the
// owner who transfers it.
type Org struct {
	ID           string    `json:"id"`
	Object       string    `json:"object"`
	Name         string    `json:"name"`
	Role         string    `json:"role"`
	PrimaryOwner bool      `json:"primary_owner"`
	CreatedAt    time.Time `json:"created_at"`
	People       int       `json:"people"` // how many people are in it
}

// AuditEvent is a line of the account's audit: what was done, to which thing, by whom, from where.
type AuditEvent struct {
	ID     string    `json:"id"`
	Object string    `json:"object"`
	At     time.Time `json:"at"`
	Action string    `json:"action"` // such as machine.create, member.role, machine.terminal.open
	Target string    `json:"target"` // what it was done to: a machine, a key, a person
	// Person is who did it, by their id and name; nil for what the API did by itself, and for Wheelmachine's staff.
	Person *AuditPerson `json:"person"`
	// By is the key (key_...) or the sign-in (ses_...) it was done with; "system" for what Wheelmachine did by itself,
	// "wheelmachine" for Wheelmachine's staff.
	By        string            `json:"by"`
	Address   string            `json:"address"` // where it came from
	RequestID string            `json:"request_id"`
	Detail    map[string]string `json:"detail"`
}

// AuditPerson is the person who did what an event says.
type AuditPerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Standing is whether the account can spend: its credit - nothing runs once it has run out - and what is left this
// month under its limit, when it has one.
type Standing struct {
	CanSpend   bool     `json:"can_spend"`
	BalanceUSD *float64 `json:"balance_usd"` // left this month under the account's limit; nil for an account with no monthly limit
	CreditUSD  *float64 `json:"credit_usd"`  // the credit left; nil for an organisation's member, who does not read it
	Reason     string   `json:"reason,omitempty"`
}

type Project struct {
	ID        string    `json:"id"`
	Object    string    `json:"object"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Key is an API key as the API shows it: never its secret.
type Key struct {
	ID            string     `json:"id"`
	Object        string     `json:"object"`
	Name          string     `json:"name"`
	Scopes        []string   `json:"scopes"`
	Projects      []string   `json:"projects"` // the projects it is limited to; empty for every project
	Shown         string     `json:"shown"`    // its first and last characters, to recognise it by
	State         string     `json:"state"`    // active, revoked or expired
	RatePerMinute *int       `json:"rate_per_minute"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
	LastUsedAt    *time.Time `json:"last_used_at"`
	// MadeBy is, in an organisation, the person who made it, by their own account's id; nil in a person's own account.
	MadeBy *string `json:"made_by"`
}

// KeyCreated is a new key with its secret, which the API shows in this answer only.
type KeyCreated struct {
	Key
	Secret string `json:"secret"`
}

// KeyCreate is what a new key may do. A key can give only scopes and projects it holds itself.
type KeyCreate struct {
	Name          string
	Scopes        []string
	Projects      []string   // none: every project the calling key reaches
	ExpiresAt     *time.Time // nil: it does not expire (or when the calling key does)
	RatePerMinute *int       // nil: the calling key's own

	IdempotencyKey string // made for the call when empty
	// the code we e-mailed for a key with full access (cloud:*), which needs one: SendConfirmCode
	ConfirmCode string
}

// What a confirm code is for: a risky step - close_account, delete_org, hand_over, make_owner, full_access_key or
// key_making_key - and what of it the code is bound to. An error that needs one says it (Error.Confirm), and SendConfirmCode is asked it.
type ConfirmAsk struct {
	Step    string `json:"step"`
	Org     string `json:"org,omitempty"`
	To      string `json:"to,omitempty"`
	Account string `json:"account,omitempty"`
}

// A confirm code on its way: what it is for, when it stops working, and the address it went to, in part.
type ConfirmCodeSent struct {
	Object string `json:"object"`
	ConfirmAsk
	ExpiresAt time.Time `json:"expires_at"`
	SentTo    string    `json:"sent_to"`
}

type Machine struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Name    string `json:"name"`
	Project string `json:"project"`
	// MadeBy is, in an organisation, the person who made it, by their own account's id. Nil in a person's own account.
	MadeBy *string `json:"made_by"`
	// HeldBy is, in an organisation, whose machine it is: the person who holds it, by name, with their role there. Nil
	// in a person's own account.
	HeldBy         *HeldBy           `json:"held_by"`
	State          string            `json:"state"`
	GPU            *GpuRef           `json:"gpu"`
	Size           *Size             `json:"size"`
	Location       *Location         `json:"location"`        // where it runs now
	CPUs           *int              `json:"cpus"`            // its CPUs, 2, 4 or 8; nil for a GPU machine
	MemoryGB       *float64          `json:"memory_gb"`       // 2 GB for each CPU; nil for a GPU machine
	SleepWhenIdle  bool              `json:"sleep_when_idle"` // it pauses by itself once idle: nothing reached it, its programs quiet
	IdleAfterS     *int              `json:"idle_after_s"`    // the idle seconds before it does; nil when it never pauses by itself
	Labels         map[string]string `json:"labels"`
	Secrets        []string          `json:"secrets"`      // the names of the secrets its commands get, never their values
	AIKeys         []string          `json:"ai_keys"`      // the account's AI keys that reach it, by name: on every machine
	Network        Network           `json:"network"`      // what it may reach on the internet
	FilesSealed    bool              `json:"files_sealed"` // its files in storage are encrypted
	RunningSeconds float64           `json:"running_seconds"`
	DiskBytes      int64             `json:"disk_bytes,omitempty"`
	CPUCores       float64           `json:"cpu_cores,omitempty"`
	MemoryBytes    int64             `json:"memory_bytes,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	LastUsedAt     *time.Time        `json:"last_used_at"`
	ReadyMS        *float64          `json:"ready_ms,omitempty"` // how long making it ready took, when this answer did
	Failure        string            `json:"failure,omitempty"`  // why it never came up, when its state is failed
	PauseReason    *PauseReason      `json:"pause_reason"`       // why it is paused, while a limit holds it so; nil otherwise
	// GivenBy is, in an organisation, who handed it off to the person who holds it, forked it or made it for them: they
	// watch it and may take it back. Nil when nobody did.
	GivenBy *PersonName `json:"given_by"`
	// Type is the team's machine type it was made from, by id; nil for none.
	Type *string `json:"type"`
	// PutAwayAfterS is how long nobody may use it before it is archived; nil for no time of its own.
	PutAwayAfterS *int `json:"put_away_after_s"`
}

// MachineType is one of a team's kinds of machine, saved by its organisation's owners and admins: what a machine made
// from it is.
type MachineType struct {
	ID            string   `json:"id"`
	Object        string   `json:"object"`
	Project       string   `json:"project"`
	Name          string   `json:"name"`
	CPUs          *int     `json:"cpus"` // nil for a GPU machine
	GPU           *string  `json:"gpu"`
	Apps          []string `json:"apps"` // the apps it has on; nil: those a new machine has on
	Network       Network  `json:"network"`
	SleepWhenIdle bool     `json:"sleep_when_idle"`
	IdleAfterS    *int     `json:"idle_after_s"`
	PutAwayAfterS *int     `json:"put_away_after_s"`
	Repos         []string `json:"repos"`   // the team's repositories its machines reach, by grant; nil: every one
	Folders       []string `json:"folders"` // the team's folders its machines reach, by id; nil: every one
}

// MachineBatchCreate makes machines of one of a team's types for its people: those named in People - each by their id,
// their e-mail address, or @ and their handle - or Everyone in the team; Each for each of them (0: one); named by
// the Names pattern of <type>, <handle> and <n> (empty: the API's "<type>-<handle>").
type MachineBatchCreate struct {
	Project  string            `json:"project,omitempty"`
	Type     string            `json:"type"`
	People   []string          `json:"people,omitempty"`
	Everyone bool              `json:"everyone,omitempty"`
	Each     int               `json:"each,omitempty"`
	Names    string            `json:"names,omitempty"`
	Region   string            `json:"region,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	WaitS    *float64          `json:"wait_s,omitempty"`

	IdempotencyKey string `json:"-"` // made for the call when empty
}

// MachineBatch is each machine of a batch, in the order asked: made, on its way, or refused.
type MachineBatch struct {
	Object  string         `json:"object"`
	Project string         `json:"project"`
	Type    string         `json:"type"`
	Data    []BatchMachine `json:"data"`
	Made    int            `json:"made"`
	Refused int            `json:"refused"`
}

// BatchMachine is one machine of a batch: whose it is, its name, and how it went - Status 201 made, 202 on its way
// (WaitForMachine follows it), else refused, with why in Err.
type BatchMachine struct {
	Person  PersonName `json:"person"`
	Name    string     `json:"name"`
	Status  int        `json:"status"`
	Machine *Machine   `json:"machine"`
	Err     *Error     `json:"-"`
}

func (b *BatchMachine) UnmarshalJSON(raw []byte) error {
	type plain BatchMachine
	var said struct {
		plain
		Error *problem `json:"error"`
	}
	if err := json.Unmarshal(raw, &said); err != nil {
		return err
	}
	*b = BatchMachine(said.plain)
	if p := said.Error; p != nil {
		b.Err = &Error{Status: b.Status, Code: p.Code, Title: p.Title, Detail: p.Detail, Type: p.Type, RequestID: p.RequestID, Limit: p.Limit}
	}
	return nil
}

// PauseReason is why a machine is paused, while a limit holds it so: the limit's words as the caller is told them,
// and the limit itself.
type PauseReason struct {
	Why   string    `json:"why"`
	Limit *LimitHit `json:"limit"`
}

type GpuRef struct {
	ID       string `json:"id"`
	Model    string `json:"model,omitempty"`
	Count    int    `json:"count,omitempty"`
	MemoryGB int    `json:"memory_gb,omitempty"`
}

type Size struct {
	CPUs     float64 `json:"cpus"`
	MemoryGB float64 `json:"memory_gb"`
	DiskGB   float64 `json:"disk_gb,omitempty"`
}

type Location struct {
	City    string `json:"city,omitempty"`
	Country string `json:"country,omitempty"` // ISO 3166-1 alpha-2
}

// MachineCreate is what a new machine is made from; every field may be left out.
type MachineCreate struct {
	Name    string            `json:"name,omitempty"`
	Project string            `json:"project,omitempty"` // the account's first project when left out
	Region  string            `json:"region,omitempty"`  // a Region's id, to run near
	GPU     string            `json:"gpu,omitempty"`     // a GpuOffer's id; needs the cloud:gpus scope
	CPUs    int               `json:"cpus,omitempty"`    // 2, 4 or 8 with 2 GB each; 0: the API's 2. Not with GPU
	Labels  map[string]string `json:"labels,omitempty"`
	Secrets []string          `json:"secrets,omitempty"` // secrets its commands get; needs cloud:secrets:write
	Network *Network          `json:"network,omitempty"` // what it may reach on the internet (open when left out)
	// SleepWhenIdle false keeps it running, and charged, until it is paused or stopped; nil or true lets it pause by
	// itself once it is idle for IdleAfterS seconds (nil: the API's 300) - nothing reached it, its programs quiet.
	SleepWhenIdle *bool `json:"sleep_when_idle,omitempty"`
	IdleAfterS    *int  `json:"idle_after_s,omitempty"`
	// PutAwayAfterS archives it once nobody has used it this long, in seconds (an hour to 30 days); nil for no time.
	PutAwayAfterS *int `json:"put_away_after_s,omitempty"`
	// Type makes it as one of its team's machine types says, by the type's id or name: its size, internet, sleep and
	// put-away time are the type's then, and are not given beside it.
	Type string `json:"type,omitempty"`
	// WaitS is how long the API waits for the machine to run before answering it is still starting (202); nil
	// leaves it to the API (60 seconds).
	WaitS *float64 `json:"wait_s,omitempty"`

	IdempotencyKey string `json:"-"` // made for the call when empty
}

// MachineUpdate renames a machine, changes its labels or says which secrets it gets. Labels merge: a label given nil
// is taken off. Secrets, when not nil, is every secret it is to get: the list takes the place of the one it had, and
// an empty one takes them all away.
type MachineUpdate struct {
	Name    *string            `json:"name,omitempty"`
	Labels  map[string]*string `json:"labels,omitempty"`
	Secrets *[]string          `json:"secrets,omitempty"`
	Network *Network           `json:"network,omitempty"` // what it may reach on the internet, said whole; applied at once
}

// Network is what a machine may reach on the internet: Internet is "open" (the whole internet),
// "closed" (no outbound at all), or "allowed", where Allow lists the names (exact or a leading *.) for HTTP and
// HTTPS and the IPv4/IPv6 ranges (CIDRs) for other ports it may reach. Allow is empty for open and closed.
type Network struct {
	Internet string   `json:"internet"`
	Allow    []string `json:"allow"`
}

// HeldBy is whose a machine of an organisation is: the person who holds it, by their id and name, with their role
// there - nil once they are no longer in it - and their level in Level when they manage the machine's team.
type HeldBy struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Handle string  `json:"handle"` // their @handle, without its @
	Role   *string `json:"role"`
	Level  *string `json:"level"`
}

// MachineFork is how a fork differs from its original: its name is the original's with -fork after it, and its labels
// are the original's, when left out.
type MachineFork struct {
	Name   *string           `json:"name,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	// Backup makes the fork from one of the machine's backups - its id, or "latest" for its newest - starting afresh
	// from its files as they were then; left out, the fork is of the machine as it is now.
	Backup string `json:"backup,omitempty"`
	// CPUs is, with Backup only, the fork's CPUs: 2, 4 or 8, with 2 GB each; 0: the original's. A fork of the
	// machine as it is now carries what is in its memory, so it is always the original's size. Not for a GPU machine.
	CPUs  int      `json:"cpus,omitempty"`
	WaitS *float64 `json:"wait_s,omitempty"`

	IdempotencyKey string `json:"-"`
}

// Backup is a machine's files kept as they were at a moment, not what ran in it: a fork of the machine is made from
// one (MachineFork.Backup), at its size or another.
type Backup struct {
	ID        string    `json:"id"`
	Object    string    `json:"object"`
	Machine   string    `json:"machine"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// BackupList is backups, the newest first.
type BackupList struct {
	Object string   `json:"object"`
	Data   []Backup `json:"data"`
}

// ExecRequest is a command to run in a machine: Command (the words, no shell) or Shell (a line for sh -lc), not both.
type ExecRequest struct {
	Command      []string          `json:"command,omitempty"`
	Shell        string            `json:"shell,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Cwd          string            `json:"cwd,omitempty"`  // where it starts; the work folder when left out
	User         string            `json:"user,omitempty"` // "user" (the default) or "root", inside the machine only
	BinaryOutput bool              `json:"binary_output,omitempty"`
}

// ExecResult is how a command ended and what it wrote. Stdout is in StdoutBase64 when binary output was asked for.
type ExecResult struct {
	ExitCode     int     `json:"exit_code"`
	Stdout       string  `json:"stdout,omitempty"`
	StdoutBase64 string  `json:"stdout_base64,omitempty"`
	Stderr       string  `json:"stderr"`
	DurationMS   float64 `json:"duration_ms"`
	Truncated    bool    `json:"truncated"` // the output was longer than 8 MiB and was cut there
}

// MachineMetrics is what a machine used, a point a step from the time asked for on. A point a minute is kept, for
// up to the last day; KeptSince is how far back that goes.
type MachineMetrics struct {
	Object    string         `json:"object"`
	Machine   string         `json:"machine"`
	StepS     int            `json:"step_s"`
	CPUs      *float64       `json:"cpus"` // what CPUPercent is a share of
	KeptSince *time.Time     `json:"kept_since"`
	Points    []MetricsPoint `json:"points"`
}

// MetricsPoint is one step of a machine. A number that could not be read is nil, and so are the rates of a step
// its readings cover none of.
type MetricsPoint struct {
	At               time.Time `json:"at"`
	CPUPercent       *float64  `json:"cpu_percent"`        // its CPUs' use, a share of its own size
	MemoryBytes      *float64  `json:"memory_bytes"`       // the most it held in the step
	MemoryLimitBytes *float64  `json:"memory_limit_bytes"` // the most it may hold
	DiskReadBps      *float64  `json:"disk_read_bps"`
	DiskWriteBps     *float64  `json:"disk_write_bps"`
	NetRxBps         *float64  `json:"net_rx_bps"`
	NetTxBps         *float64  `json:"net_tx_bps"`
}

// Secret is a secret as the API shows it: its name, the machines given it, how it is kept and when it was set - never
// its value, which the API does not show again once it is set.
type Secret struct {
	Object   string   `json:"object"`
	Name     string   `json:"name"`
	Machines []string `json:"machines"` // the machines that name it, that the key in use can see
	// Outside is set when it is kept outside the machines: its value never enters one: it is added on the way out,
	// only to requests going to its hosts. Nil for an ordinary secret.
	Outside *Outside `json:"outside"`
	// EveryMachine: it is on every machine of the account that may send it somewhere, not only on those that name it.
	// An AI key is, unless it was set to be only on the machines given it.
	EveryMachine bool      `json:"every_machine"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"` // when its value was last set
}

// AIKey says whether a secret is an AI key: kept outside the machines by its name's preset.
func (s Secret) AIKey() bool { return s.Outside != nil && s.Outside.Preset != nil }

// Outside is how a secret kept outside the machines is sent: where, and in which headers - never its value.
type Outside struct {
	Preset  *string      `json:"preset"` // the preset it follows (SecretPresets); nil for a policy of its own
	Hosts   []string     `json:"hosts"`
	Header  string       `json:"header"` // the first of Headers
	Format  string       `json:"format"`
	Headers []HeaderRule `json:"headers"`
	Query   []string     `json:"query"` // query parameters whose value may carry it
}

// HeaderRule is a request header a secret kept outside goes in, and how it is written there: {value} once.
type HeaderRule struct {
	Name   string `json:"name"`
	Format string `json:"format"`
}

// SecretPreset is a model provider's key as the API knows it by its names: set under one of Names, a secret is kept
// outside the machines and sent only to Hosts, in Headers.
type SecretPreset struct {
	Object  string       `json:"object"`
	ID      string       `json:"id"`
	Title   string       `json:"title"` // the provider, as people know it
	Names   []string     `json:"names"`
	Hosts   []string     `json:"hosts"`
	Headers []HeaderRule `json:"headers"`
	Query   []string     `json:"query"`
}

// SecretSet says how a secret is kept, beside its value. The zero value leaves it to the API: a name with a preset is
// an AI key, kept outside the machines and on every one of them; any other is an ordinary secret, on the machines
// given it.
type SecretSet struct {
	// Inside keeps it an ordinary secret, even under a preset's name: its value reaches the machines' commands,
	// terminals and SSH sessions, for a tool that cannot work with a placeholder.
	Inside bool
	// Outside, when not nil, keeps it outside the machines by a policy of its own.
	Outside *OutsideSet
	// OnlyGiven keeps it on the machines given it by name, never on every machine.
	OnlyGiven bool
}

// OutsideSet is a policy of its own for a secret kept outside the machines.
type OutsideSet struct {
	Hosts  []string `json:"hosts"`
	Header string   `json:"header"`
	Format string   `json:"format,omitempty"` // "{value}" when empty
}

// Port is a port of a machine open at a web address of its own, for a program listening on it. A private address
// answers a call with an API key that can see the machine, and a browser that opened a link from LinkPort; a public
// one answers anyone.
type Port struct {
	Object    string    `json:"object"`
	Machine   string    `json:"machine"`
	Port      int       `json:"port"`
	Public    bool      `json:"public"`
	URL       string    `json:"url"` // https://<port>-<the machine's id without m_>.run.wheelmachine.ai
	CreatedAt time.Time `json:"created_at"`
}

// PortOpen is a port to open, private unless Public. A port open already is made public or private as asked.
type PortOpen struct {
	Port   int  `json:"port"` // 1 to 65535, but not one the machine itself uses (5900, 6080, 7070, 9222, 9223, 9224)
	Public bool `json:"public"`
}

// PortLinkCreate is how a link to a port's private address is made; both fields may be left out.
type PortLinkCreate struct {
	AccessS int    `json:"access_s,omitempty"` // how long the browser that opens it may reach the address, 60 to 3600; 0: 1800
	Path    string `json:"path,omitempty"`     // where on the address it lands, beginning with / (a query may follow); "": /
}

// PortLink is a link that lets one browser into a port's private address. URL opens once, before ExpiresAt (five
// minutes after it was made), and gives the browser that opens it access to that address alone for AccessS seconds.
// It is shown in this answer only.
type PortLink struct {
	Object    string    `json:"object"`
	Machine   string    `json:"machine"`
	Port      int       `json:"port"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	AccessS   int       `json:"access_s"`
}

// App is an app in a machine, as the machine says it is now. Kind is "web" (a page at the machine's private web
// address for its port), "service" (reached from inside the machine alone), "agent" (an agent with a session of its
// own in the machine's terminal, running Command; Running says whether the session runs now) or
// "terminal" (an agent run in its terminal by typing Command). On and Running
// are nil for a terminal agent, which runs while it is typed. KeysChanged, for an agent, says whether its session,
// which runs, began with other AI keys than the machine has now: turned off and on again, its next session begins
// with them. It is nil when the machine does not say it.
//
// Link, with ExpiresAt and AccessS, comes in the answer to turning a web app on and to LinkApp: it lets one browser
// into the app's address once, within five minutes, landing on its page. Token comes in the answer to turning on an
// app that has one of its own, such as an agent's gateway. Each is shown in that answer only.
type App struct {
	Object    string     `json:"object"`
	Machine   string     `json:"machine"`
	Name      string     `json:"name"`
	Title     string     `json:"title"`
	Kind      string     `json:"kind"`
	On        *bool      `json:"on"`
	Running   *bool      `json:"running"`
	Port      *int       `json:"port"`    // where it listens inside the machine, when that is said
	Path      *string    `json:"path"`    // a web app's own page on its address
	URL       *string    `json:"url"`     // a web app's private address
	Command   *string    `json:"command"` // what an agent's session runs, or what to type in the terminal for a terminal agent
	Link      string     `json:"link,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	AccessS   int        `json:"access_s,omitempty"`
	Token     string     `json:"token,omitempty"`
	// for an agent: its session began with other AI keys than the machine has now
	KeysChanged *bool `json:"keys_changed,omitempty"`
	// for an agent, and a web app with an agent of its own, as a machine made since models were chosen says them: the
	// providers whose AI keys it can use; what it last started on, provider/model (nil: not started yet, or a model of its
	// own); who chose that (chosen, team, default or agent); and whether it runs on another model than the one chosen
	// for it now
	Suppliers    []string `json:"suppliers,omitempty"`
	Model        *string  `json:"model,omitempty"`
	ModelBy      *string  `json:"model_by,omitempty"`
	ModelChanged *bool    `json:"model_changed,omitempty"`
}

// Model is a model one of the account's AI keys can use, as its provider lists it: its id is its provider and the provider's
// own id for it (openai/gpt-6.1-sol), Agents the agents of the machines that can use it, Price what it costs as the
// provider that bills for it lists it, and Intelligence its score on LMArena's text leaderboard - each nil where it is
// not known.
type Model struct {
	Object       string             `json:"object"`
	ID           string             `json:"id"`
	Supplier     string             `json:"supplier"`
	Model        string             `json:"model"`
	Name         string             `json:"name"`
	CreatedAt    *time.Time         `json:"created_at"`
	RetiringAt   *time.Time         `json:"retiring_at"`
	Agents       []string           `json:"agents"`
	Price        *ModelPrice        `json:"price"`
	Intelligence *ModelIntelligence `json:"intelligence"`
}

// ModelPrice is a model's list price: a million tokens in and a million out, in US dollars, as the provider that bills
// for it (Source) lists it at URL, checked on AsOf (a day). It is paid to that provider with the account's own key:
// Wheelmachine charges nothing for a model.
type ModelPrice struct {
	InputPerMillionUSD  float64 `json:"input_per_million_usd"`
	OutputPerMillionUSD float64 `json:"output_per_million_usd"`
	Source              string  `json:"source"`
	URL                 string  `json:"url"`
	AsOf                string  `json:"as_of"`
}

// ModelIntelligence is a model's score on LMArena's text leaderboard: the best it has there at any effort, as a whole
// number, higher better; the board (Source) at URL, its licence, and the day it was published (AsOf).
type ModelIntelligence struct {
	Score   float64 `json:"score"`
	Source  string  `json:"source"`
	URL     string  `json:"url"`
	License string  `json:"license"`
	AsOf    string  `json:"as_of"`
}

// ModelSupplier is a provider of the account's AI keys, and how its list stands: listed, refused (the key), busy,
// unreachable, or unlisted (no agent works with its keys yet); Detail says it in words when it is not listed.
type ModelSupplier struct {
	Object   string     `json:"object"`
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Key      string     `json:"key"`
	Whose    string     `json:"whose"`
	State    string     `json:"state"`
	ListedAt *time.Time `json:"listed_at"`
	Detail   *string    `json:"detail"`
}

// ModelList is the models of the account's AI keys, and their providers.
type ModelList struct {
	Object    string          `json:"object"`
	Data      []Model         `json:"data"`
	Suppliers []ModelSupplier `json:"suppliers"`
	// Allowed is the list of the models the caller may use that applies to their choices - its models alone are listed -
	// and nil when none does.
	Allowed *AllowedModels `json:"allowed"`
}

// AllowedModels is a list of the models an organisation's people may use, as its owners and admins keep it: for the
// whole organisation, a team, a role (a person's level in the team: member, approver, manager or full) or one person -
// by id and by name - its models in order, and who set it, when.
type AllowedModels struct {
	Object string      `json:"object"`
	Level  string      `json:"level"`
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Models []string    `json:"models"`
	SetBy  *PersonName `json:"set_by"`
	SetAt  time.Time   `json:"set_at"`
}

// AllowedModelsList is every list an organisation keeps, the widest first.
type AllowedModelsList struct {
	Object string          `json:"object"`
	Data   []AllowedModels `json:"data"`
}

// Agent is an agent of the account's machines, and the model it starts on: the providers it can use (each with the
// model it starts on with that key, nil: its own), the model chosen for it and by whom (you, or every_agent), what it
// starts on with the caller's keys, how that stands in a word, and in a sentence where it is not as chosen.
type Agent struct {
	Object    string          `json:"object"`
	Name      string          `json:"name"`
	Title     string          `json:"title"`
	Suppliers []AgentSupplier `json:"suppliers"`
	Model     *string         `json:"model"`
	ChosenBy  *string         `json:"chosen_by"`
	StartsOn  *string         `json:"starts_on"`
	State     string          `json:"state"`
	Note      *string         `json:"note"`
}

// AgentSupplier is a provider an agent can use, and the model it starts on with its key when none is chosen.
type AgentSupplier struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Default *string `json:"default"`
}

// AgentList is the agents of the machines, and the one model chosen for every agent (nil: none).
type AgentList struct {
	Object     string      `json:"object"`
	Data       []Agent     `json:"data"`
	EveryAgent *EveryAgent `json:"every_agent"`
	// Allowed is the list of the models the caller may use that applies to their choices in the team; nil when none does.
	Allowed *AllowedModels `json:"allowed"`
}

// EveryAgent is the one model chosen for every agent, and by whom.
type EveryAgent struct {
	Model    string `json:"model"`
	ChosenBy string `json:"chosen_by"`
}

// AppLinkCreate is how the link into a web app is made; AccessS may be left out.
type AppLinkCreate struct {
	AccessS int `json:"access_s,omitempty"` // how long the browser that opens it may reach the address, 60 to 3600; 0: 1800
}

// SSHCertificateRequest asks the API to sign an SSH certificate: a public key, the machine it is for, and how long
// it may live (seconds, one hour at most and the default).
type SSHCertificateRequest struct {
	PublicKey string `json:"public_key"`
	Machine   string `json:"machine"`
	TTLs      *int   `json:"ttl_s,omitempty"`
}

// SSHCertificate is a signed certificate for real SSH into a machine (scp, sftp, editors' remote-SSH).
type SSHCertificate struct {
	Object        string `json:"object"`
	Certificate   string `json:"certificate"`
	Principal     string `json:"principal"`
	SSHUser       string `json:"ssh_user"`
	ExpiresAt     string `json:"expires_at"`
	CAPublicKey   string `json:"ca_public_key"`
	HostPublicKey string `json:"host_public_key"`
	SSHHost       string `json:"ssh_host"`
}

// Region is a place a machine can be asked to run near.
type Region struct {
	ID      string `json:"id"`
	City    string `json:"city"`
	Country string `json:"country"`
}

// GpuList is the GPUs on offer now. ForSale is false while GPU machines are not open to every account yet.
type GpuList struct {
	Object  string     `json:"object"`
	ForSale bool       `json:"for_sale"`
	Data    []GpuOffer `json:"data"`
}

type GpuOffer struct {
	ID              string    `json:"id"`
	Model           string    `json:"model"`
	Count           int       `json:"count"`
	MemoryGB        int       `json:"memory_gb"`
	CPUs            int       `json:"cpus,omitempty"`
	RAMGB           float64   `json:"ram_gb,omitempty"`
	DiskGB          float64   `json:"disk_gb,omitempty"`
	Location        *Location `json:"location,omitempty"`
	PricePerHourUSD float64   `json:"price_per_hour_usd"`
	Interruptible   bool      `json:"interruptible"` // can be interrupted at any time; cheaper for it
	Available       bool      `json:"available"`
}

// LimitHit is the limit that stopped a call: Wheelmachine's for the account ("wheelmachine"), the account's own
// ("account"), a team's budget ("team") or a person's own ("person"); of what - dollars this month, machines held,
// CPUs at once, GPU machines - at what and how much is used (nil when the caller is not shown them), when a limit for
// the month lifts, and who can raise it.
type LimitHit struct {
	Scope    string     `json:"scope"`
	Measure  string     `json:"measure"`
	Limit    *float64   `json:"limit"`
	Used     *float64   `json:"used"`
	ResetsAt *time.Time `json:"resets_at"`
	RaisedBy []string   `json:"raised_by"`
}

// UsageLimits is every limit for the month that applies to the account, as the caller may see it.
type UsageLimits struct {
	Object        string        `json:"object"`
	Month         string        `json:"month"`
	ResetsAt      time.Time     `json:"resets_at"`
	AlertPercents []int         `json:"alert_percents"`
	Account       AccountLimit  `json:"account"`
	Teams         []TeamLimit   `json:"teams"`
	People        []PersonLimit `json:"people"`
	Yours         []LimitNear   `json:"yours"`
	Passed        []LimitPassed `json:"passed"`
}

// AccountLimit is the account's own limit for the month and Wheelmachine's under it: the lower holds (InForceUSD).
// Amounts are nil to those not shown them; Limited says to anyone whether a limit holds the account.
type AccountLimit struct {
	Name         string      `json:"name"`
	Kind         string      `json:"kind"`
	MonthCapUSD  *float64    `json:"month_cap_usd"`
	InForceUSD   *float64    `json:"in_force_usd"`
	InForce      string      `json:"in_force"`
	SpentUSD     *float64    `json:"spent_usd"`
	Percent      *int        `json:"percent"`
	Limited      bool        `json:"limited"`
	SetBy        *PersonName `json:"set_by"`
	SetAt        *time.Time  `json:"set_at"`
	Wheelmachine struct {
		MonthCapUSD *float64 `json:"month_cap_usd"`
		MaxMachines int      `json:"max_machines"`
		MaxVCPUs    *int     `json:"max_vcpus"`
	} `json:"wheelmachine"`
	ChangedBy []string `json:"changed_by"`
	YouChange bool     `json:"you_change"`
}

// TeamLimit is a team's budget for the month, and what it spent to those who see it.
type TeamLimit struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	MonthBudgetUSD *float64 `json:"month_budget_usd"`
	SpentUSD       *float64 `json:"spent_usd"`
	Percent        *int     `json:"percent"`
	Yours          bool     `json:"yours"`
	ChangedBy      []string `json:"changed_by"`
	YouChange      bool     `json:"you_change"`
}

// PersonLimit is a person's own limits in an organisation.
type PersonLimit struct {
	Person      PersonName `json:"person"`
	Role        string     `json:"role"`
	MonthCapUSD *float64   `json:"month_cap_usd"`
	SpentUSD    *float64   `json:"spent_usd"`
	Percent     *int       `json:"percent"`
	MaxMachines *int       `json:"max_machines"`
	MaxVCPUs    *int       `json:"max_vcpus"`
	GPUs        bool       `json:"gpus"`
	ChangedBy   []string   `json:"changed_by"`
	YouChange   bool       `json:"you_change"`
}

// LimitNear is a limit for the month that applies to the caller, and how near it is.
type LimitNear struct {
	Scope    string   `json:"scope"`
	Name     string   `json:"name"`
	LimitUSD *float64 `json:"limit_usd"`
	SpentUSD *float64 `json:"spent_usd"`
	Percent  *int     `json:"percent"`
	Reached  bool     `json:"reached"`
}

// LimitPassed is a threshold of a limit told this month: 50, 75, 90 or 100 percent.
type LimitPassed struct {
	Scope   string    `json:"scope"`
	Name    string    `json:"name"`
	Percent int       `json:"percent"`
	At      time.Time `json:"at"`
}

// Usage is each machine's running time and cost in a month.
type Usage struct {
	Object   string      `json:"object"`
	Month    string      `json:"month"`
	TotalUSD float64     `json:"total_usd"`
	Machines []UsageLine `json:"machines"`
	// what the account's folders came to in the month: to those who watch its money, nil to anyone else
	FoldersUSD *float64 `json:"folders_usd,omitempty"`
}

type UsageLine struct {
	Machine string `json:"machine"`
	Name    string `json:"name,omitempty"` // the machine's name
	Project string `json:"project,omitempty"`
	// MadeBy is, in an organisation, the person who made the machine; nil in a person's own account.
	MadeBy *string `json:"made_by"`
	// HeldBy is, in an organisation, whose machine it is; nil in a person's own account.
	HeldBy         *HeldBy `json:"held_by"`
	RunningSeconds float64 `json:"running_seconds"`
	USD            float64 `json:"usd"`
}
