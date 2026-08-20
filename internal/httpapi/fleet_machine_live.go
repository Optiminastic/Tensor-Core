package httpapi

// GET /machine-fleet/:id/live - a printer's live telemetry, read through from
// BambuBuddy on demand.
//
// Deliberately NOT stored. Temperatures, fan speeds and drying countdowns are
// true for a second; persisting them would mean a database write per machine
// per poll and a UI that shows a stale number confidently. The machines table
// keeps only what Tensor schedules against (status, layer progress, loaded
// filament), and everything else is read live or not shown at all.

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
)

type liveTemperature struct {
	Current float64 `json:"current"`
	Target  float64 `json:"target"`
	Heating bool    `json:"heating"`
}

type liveNozzle struct {
	Position       string  `json:"position"` // "left" | "right"
	DiameterMM     float64 `json:"diameter_mm"`
	Type           string  `json:"type"`
	HighFlow       bool    `json:"high_flow"`
	Wear           int     `json:"wear"`
	FilamentColour string  `json:"filament_colour"`
}

type liveTray struct {
	Slot      int    `json:"slot"`
	Type      string `json:"type"`
	Colour    string `json:"colour"`
	RemainPct int    `json:"remain_pct"`
	Loaded    bool   `json:"loaded"`
}

type liveAMS struct {
	ID           int        `json:"id"`
	HumidityPct  int        `json:"humidity_pct"`
	TemperatureC float64    `json:"temperature_c"`
	Drying       bool       `json:"drying"`
	DryFilament  string     `json:"dry_filament"`
	DryMinutes   int        `json:"dry_minutes_left"`
	Trays        []liveTray `json:"trays"`
}

// liveMachineResponse is everything the machine detail screen shows that comes
// from the printer rather than from Tensor.
type liveMachineResponse struct {
	Connected       bool    `json:"connected"`
	State           string  `json:"state"`
	StateLabel      string  `json:"state_label"`
	CurrentPrint    string  `json:"current_print"`
	ProgressPercent float64 `json:"progress_percent"`
	RemainingMin    int     `json:"remaining_minutes"`
	LayerNum        int     `json:"layer_num"`
	TotalLayers     int     `json:"total_layers"`

	WifiDbm         int    `json:"wifi_dbm"`
	FirmwareVersion string `json:"firmware_version"`
	DoorOpen        bool   `json:"door_open"`
	ChamberLight    bool   `json:"chamber_light"`
	SDCard          bool   `json:"sdcard"`

	NozzleLeft  *liveTemperature `json:"nozzle_left"`
	NozzleRight *liveTemperature `json:"nozzle_right"`
	Bed         liveTemperature  `json:"bed"`
	Chamber     liveTemperature  `json:"chamber"`

	FanCoolingPct   int `json:"fan_cooling_pct"`
	FanAux1Pct      int `json:"fan_aux1_pct"`
	FanAux2Pct      int `json:"fan_aux2_pct"`
	FanHeatbreakPct int `json:"fan_heatbreak_pct"`

	Nozzles []liveNozzle `json:"nozzles"`
	AMS     []liveAMS    `json:"ams"`

	// HealthMessages is how many HMS notices the printer is reporting, and the
	// worst severity among them. Bambu raises routine notices here too, so a
	// non-zero count does NOT mean the printer is faulty - which is why the
	// severity is carried rather than just the count.
	HealthMessages int `json:"health_messages"`
	WorstSeverity  int `json:"worst_severity"`
}

func (s *Server) getFleetMachineLive(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if !s.bambu.Configured() {
		detail(c, http.StatusConflict, "BambuBuddy is not configured on this service.")
		return
	}
	ctx := c.Request.Context()

	machine, err := s.store.Q.GetFleetMachine(ctx, id)
	if err != nil {
		if isNoRows(err) {
			detail(c, http.StatusNotFound, "That machine does not exist.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not load the machine.")
		return
	}

	printerID, err := s.bambuPrinterIDFor(ctx, machine.MachineID)
	if err != nil {
		detail(c, http.StatusBadGateway, "Could not reach BambuBuddy.")
		return
	}
	if printerID < 0 {
		detail(c, http.StatusNotFound, "BambuBuddy does not have this printer.")
		return
	}

	st, err := s.bambu.GetStatus(ctx, printerID)
	if err != nil {
		detail(c, http.StatusBadGateway, "Could not read the printer's status.")
		return
	}
	c.JSON(http.StatusOK, liveDTO(st))
}

func liveDTO(st bambubuddy.Status) liveMachineResponse {
	t := st.Temperatures
	out := liveMachineResponse{
		Connected: st.Connected, State: st.State, StateLabel: stateLabel(st.State),
		CurrentPrint: st.CurrentPrint, ProgressPercent: st.Progress,
		RemainingMin: st.RemainingTime, LayerNum: st.LayerNum, TotalLayers: st.TotalLayers,
		WifiDbm: st.WifiSignal, FirmwareVersion: st.FirmwareVersion,
		DoorOpen: st.DoorOpen, ChamberLight: st.ChamberLight, SDCard: st.SDCard,
		Bed:           liveTemperature{Current: t.Bed, Target: t.BedTarget, Heating: t.BedHeating},
		Chamber:       liveTemperature{Current: t.Chamber, Target: t.ChamberTarget, Heating: t.ChamberHeating},
		FanCoolingPct: st.CoolingFanSpeed, FanAux1Pct: st.BigFan1Speed,
		FanAux2Pct: st.BigFan2Speed, FanHeatbreakPct: st.HeatbreakFanSpeed,
		Nozzles: make([]liveNozzle, 0, 2), AMS: make([]liveAMS, 0, len(st.AMS)),
		HealthMessages: len(st.HMSErrors), WorstSeverity: worstSeverity(st.HMSErrors),
	}

	out.NozzleLeft = &liveTemperature{Current: t.Nozzle, Target: t.NozzleTarget, Heating: t.NozzleHeating}
	// Only a genuinely dual-nozzle machine reports a second hot end. A single
	// one would otherwise render a permanent "R 0.0" that does not exist.
	if len(st.Nozzles) > 1 || t.Nozzle2 > 0 {
		out.NozzleRight = &liveTemperature{Current: t.Nozzle2, Target: t.Nozzle2Target, Heating: t.Nozzle2Heating}
	}

	rack := st.NozzleRack
	for i, n := range st.Nozzles {
		ln := liveNozzle{
			Position: "left", DiameterMM: n.DiameterMM(), Type: n.Type,
			HighFlow: nozzleFlow(n.Type) == "high_flow",
		}
		if i == 1 {
			ln.Position = "right"
		}
		if i < len(rack) {
			ln.Wear = rack[i].Wear
			ln.FilamentColour = hexColour(rack[i].FilamentColour)
		}
		out.Nozzles = append(out.Nozzles, ln)
	}

	for _, a := range st.AMS {
		unit := liveAMS{
			ID: a.ID, HumidityPct: a.Humidity, TemperatureC: a.Temp,
			// dry_time is minutes remaining; it stays non-zero after a cycle
			// ends, so the status flag is what decides whether it is drying.
			Drying:      a.DryStatus != 0 || a.DryTime > 0,
			DryFilament: a.DryFilament, DryMinutes: a.DryTime,
			Trays: make([]liveTray, 0, len(a.Trays)),
		}
		for _, tr := range a.Trays {
			unit.Trays = append(unit.Trays, liveTray{
				Slot: tr.ID, Type: strings.TrimSpace(tr.Type),
				Colour: hexColour(tr.Colour), RemainPct: tr.Remain,
				Loaded: tr.Exists && strings.TrimSpace(tr.Type) != "",
			})
		}
		out.AMS = append(out.AMS, unit)
	}
	return out
}

// stateLabel turns Bambu's state code into the words an operator reads.
func stateLabel(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "RUNNING":
		return "Printing"
	case "FINISH":
		return "Finished"
	case "PAUSE":
		return "Paused"
	case "PREPARE":
		return "Preparing"
	case "IDLE", "":
		return "Ready to print"
	default:
		return state
	}
}

func worstSeverity(errs []bambubuddy.HMSError) int {
	worst := 0
	for _, e := range errs {
		if e.Severity > worst {
			worst = e.Severity
		}
	}
	return worst
}

func (s *Server) registerFleetMachineLive(g *gin.RouterGroup) {
	g.GET("/:id/live", s.guards.RequirePermission(auth.MachineRead.Key()), s.getFleetMachineLive)
}

// bambuPrinterIDFor resolves a Tensor fleet machine to BambuBuddy's own printer
// id, matching on serial number - the same key the fleet sync uses, so a
// printer renamed on either side still resolves. Returns -1 when BambuBuddy
// does not have it.
func (s *Server) bambuPrinterIDFor(ctx context.Context, machineCode string) (int, error) {
	printers, err := s.bambu.ListPrinters(ctx)
	if err != nil {
		return -1, err
	}
	for _, p := range printers {
		if fleetCode(p) == machineCode {
			return p.ID, nil
		}
	}
	return -1, nil
}
