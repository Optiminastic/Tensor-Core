// Package bambubuddy talks to a BambuBuddy instance - the local service that
// holds the MQTT connection to each physical Bambu printer and exposes their
// live state over HTTP.
//
// This is the boundary between Tensor's model of the fleet (machines,
// machine_profiles) and the printers themselves. Tensor never speaks to a
// printer directly: BambuBuddy owns the connection, the credentials and the
// protocol, and this package reads from it.
//
// Deliberately read-only. Nothing here starts, pauses or stops a print. Tensor
// decides WHAT should be printed; sending a plate to a machine is a separate
// decision that needs its own review, and a package that can only read cannot
// accidentally acquire that power.
package bambubuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is a BambuBuddy HTTP client. The zero value is not usable; use New.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// requestTimeout bounds a single call. BambuBuddy answers from cached MQTT
// state rather than by polling the printer, so a slow response means the
// service itself is struggling and waiting longer will not help.
const requestTimeout = 10 * time.Second

// New builds a client. baseURL is the service root, e.g.
// "http://192.168.1.19:8000" or "http://host.tailnet.ts.net:8000".
//
// A trailing slash is trimmed because every call appends a rooted path, so
// "http://host:8000/" would otherwise produce "http://host:8000//api/v1/...".
// Whether a double slash works is up to the far end - FastAPI redirects some
// and 404s others - and "I pasted the URL with the slash the browser showed"
// is not a configuration mistake worth debugging at runtime.
//
// Surrounding whitespace goes too: a value copied out of a chat message or a
// .env file often carries a trailing space, and an unreachable host is a
// miserable way to discover it.
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		http:    &http.Client{Timeout: requestTimeout},
	}
}

// Configured reports whether the client has somewhere to call and a key to
// call it with. Callers skip the sync entirely when this is false rather than
// logging a failure every cycle on an install that has no BambuBuddy.
func (c *Client) Configured() bool {
	return c != nil && c.baseURL != "" && c.apiKey != ""
}

// Printer is one printer as BambuBuddy has it configured.
type Printer struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	SerialNumber string `json:"serial_number"`
	IPAddress    string `json:"ip_address"`
	Model        string `json:"model"`
	Location     string `json:"location"`
	IsActive     bool   `json:"is_active"`
	NozzleCount  int    `json:"nozzle_count"`
}

// Nozzle is one installed hot end.
type Nozzle struct {
	Type string `json:"nozzle_type"`
	// Diameter arrives as a string ("0.4"), not a number.
	Diameter string `json:"nozzle_diameter"`
}

// DiameterMM parses Diameter, returning 0 when it is absent or unreadable.
func (n Nozzle) DiameterMM() float64 {
	v, err := strconv.ParseFloat(n.Diameter, 64)
	if err != nil {
		return 0
	}
	return v
}

// Tray is one AMS slot.
type Tray struct {
	ID       int    `json:"id"`
	Colour   string `json:"tray_color"`
	Type     string `json:"tray_type"`
	Exists   bool   `json:"exists"`
	Remain   int    `json:"remain"`
	SubBrand string `json:"tray_sub_brands"`
}

// AMS is one filament unit and its slots.
type AMS struct {
	ID           int     `json:"id"`
	Humidity     int     `json:"humidity"`
	Temp         float64 `json:"temp"`
	SerialNumber string  `json:"serial_number"`
	// DryTime is minutes of drying left, DryStatus non-zero while a cycle runs.
	DryTime     int    `json:"dry_time"`
	DryStatus   int    `json:"dry_status"`
	DryFilament string `json:"dry_filament"`
	Trays       []Tray `json:"tray"`
}

// Temperatures are the printer's live thermals. Targets are 0 when nothing is
// being held at temperature, which is how "idle" is distinguished from
// "holding" without a separate flag.
type Temperatures struct {
	Nozzle        float64 `json:"nozzle"`
	NozzleTarget  float64 `json:"nozzle_target"`
	NozzleHeating bool    `json:"nozzle_heating"`
	// Nozzle2 is the right hot end on a dual-nozzle machine.
	Nozzle2        float64 `json:"nozzle_2"`
	Nozzle2Target  float64 `json:"nozzle_2_target"`
	Nozzle2Heating bool    `json:"nozzle_2_heating"`
	Bed            float64 `json:"bed"`
	BedTarget      float64 `json:"bed_target"`
	BedHeating     bool    `json:"bed_heating"`
	Chamber        float64 `json:"chamber"`
	ChamberTarget  float64 `json:"chamber_target"`
	ChamberHeating bool    `json:"chamber_heating"`
}

// RackNozzle is one installed hot end with its wear counter. wear is a raw
// Bambu counter, not a percentage - reported as-is rather than converted into a
// number that would imply a precision the value does not carry.
type RackNozzle struct {
	ID             int    `json:"id"`
	Type           string `json:"nozzle_type"`
	Diameter       string `json:"nozzle_diameter"`
	Wear           int    `json:"wear"`
	MaxTemp        int    `json:"max_temp"`
	FilamentColour string `json:"filament_color"`
	FilamentType   string `json:"filament_type"`
}

// Status is a printer's live state.
//
// A subset of what BambuBuddy returns (~60 fields): the ones Tensor's fleet
// view and scheduler actually use. Adding a field here is cheap; carrying all
// sixty when four are read is not.
type Status struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	Connected    bool    `json:"connected"`
	State        string  `json:"state"`
	CurrentPrint string  `json:"current_print"`
	Progress     float64 `json:"progress"`
	// RemainingTime is in minutes.
	RemainingTime   int          `json:"remaining_time"`
	LayerNum        int          `json:"layer_num"`
	TotalLayers     int          `json:"total_layers"`
	Nozzles         []Nozzle     `json:"nozzles"`
	NozzleRack      []RackNozzle `json:"nozzle_rack"`
	AMS             []AMS        `json:"ams"`
	WifiSignal      int          `json:"wifi_signal"`
	FirmwareVersion string       `json:"firmware_version"`
	DoorOpen        bool         `json:"door_open"`
	ChamberLight    bool         `json:"chamber_light"`
	SDCard          bool         `json:"sdcard"`
	SpeedLevel      int          `json:"speed_level"`
	Temperatures    Temperatures `json:"temperatures"`
	HMSErrors       []HMSError   `json:"hms_errors"`

	// Fan speeds are 0-100 percentages.
	CoolingFanSpeed   int `json:"cooling_fan_speed"`
	BigFan1Speed      int `json:"big_fan1_speed"`
	BigFan2Speed      int `json:"big_fan2_speed"`
	HeatbreakFanSpeed int `json:"heatbreak_fan_speed"`
}

// HMSError is one printer health message.
type HMSError struct {
	Code     string `json:"code"`
	FullCode string `json:"full_code"`
	// Severity is 1 (lowest) upward. BambuBuddy reports routine notices here
	// too, so a non-empty list does NOT mean the printer is broken.
	Severity int `json:"severity"`
	Module   int `json:"module"`
}

// Printing reports whether the machine is actively laying plastic.
//
// Bambu's state vocabulary is larger than Tensor's three-way idle/running/off,
// and only RUNNING means a plate is in progress. FINISH, IDLE, PREPARE and
// PAUSE are all "not printing" as far as scheduling is concerned - a finished
// plate still sitting on the bed is not occupying machine time, it is waiting
// for a human.
func (s Status) Printing() bool { return s.State == "RUNNING" }

// ListPrinters returns every printer BambuBuddy has configured.
func (c *Client) ListPrinters(ctx context.Context) ([]Printer, error) {
	var out []Printer
	if err := c.get(ctx, "/api/v1/printers/", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetStatus returns one printer's live state.
func (c *Client) GetStatus(ctx context.Context, printerID int) (Status, error) {
	var out Status
	if err := c.get(ctx, fmt.Sprintf("/api/v1/printers/%d/status", printerID), &out); err != nil {
		return Status{}, err
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("bambubuddy %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// The body may carry the reason, but it may also carry the API key back
		// in an echoed request - so the status alone is reported.
		return fmt.Errorf("bambubuddy %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}
