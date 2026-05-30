package seed

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/process"
)

// Port of fc-platform/src/shared/default_processes.rs. Seeds the
// platform's built-in example Process: an on-demand fulfilment workflow
// rendered as a single Mermaid flowchart. Operators land on the Processes
// page with one well-formed example showing the conventions (three-segment
// code, Mermaid body, tags, application=`platform`). The platform
// application is what surfaces under the Developer navigation, so the
// example is associated there via its `platform:` code prefix.
//
// Bootstrap-only infrastructure processing (no UoW, no executing
// principal) — writes go directly to the pool, same exception as the
// other seeders in this package.
//
// Idempotent: skips if a row with code = platform:fulfilment:on-demand-flow
// already exists. Once seeded, the row is editable like any other Process —
// re-running the seeder does not overwrite operator edits.

const (
	exampleProcessCode = "platform:fulfilment:on-demand-flow"
	exampleProcessName = "On-Demand Fulfilment Flow"
	exampleProcessDesc = "Reference workflow covering order placement, geocoding, picking, packing, " +
		"trip creation, vehicle assignment, execution, and the cancel / abort branches. " +
		"Edit or archive freely — the seeder only writes if no row with this code exists."
)

// exampleProcessBody is the Mermaid source for the example workflow. Edit
// here and bump a fresh DB to re-seed (the seeder is one-shot per code).
const exampleProcessBody = `flowchart TD
    Start([Customer places order]) --> OrderCreated[OrderCreated]
    OrderCreated --> GeoCheck{Address geocoded?}

    GeoCheck -- No --> GeoJob[Dispatch geocoding job]
    GeoJob --> GeoResult{Resolved?}
    GeoResult -- No --> GeoHold[Order on hold — geocoding failed]
    GeoHold --> NotifyCustomer[Notify customer]
    NotifyCustomer --> EndHold([Awaiting address fix])
    GeoResult -- Yes --> Reserve
    GeoCheck -- Yes --> Reserve[Reserve inventory at WMS]

    Reserve --> Stock{Stock available?}
    Stock -- No --> Backorder[Backorder created]
    Backorder --> EndBackorder([Backordered])
    Stock -- Yes --> Pick[Pick goods]

    Pick --> CancelEarly{Cancellation requested?}
    CancelEarly -- Yes --> ReleaseInv[Release inventory]
    ReleaseInv --> Refund[Refund customer]
    Refund --> EndCancel([Order cancelled])
    CancelEarly -- No --> Pack[Pack parcels]

    Pack --> Ready[FulfilmentReady]
    Ready --> CreateTrip[Create trip]
    CreateTrip --> AssignVehicle[Assign vehicle and driver]
    AssignVehicle --> Load[Load vehicle at depot]
    Load --> Depart[Depart depot]
    Depart --> EnRoute[En route]

    EnRoute --> AbortCheck{Abort signal?}
    AbortCheck -- Yes --> AbortReturn[Return to depot]
    AbortReturn --> ReverseLogistics[Restock inventory]
    ReverseLogistics --> EndAbort([Trip aborted])
    AbortCheck -- No --> Arrive[Arrive at customer]

    Arrive --> POD{Proof of delivery captured?}
    POD -- No --> Failed[Delivery failed]
    Failed --> Reschedule{Reschedule attempt?}
    Reschedule -- Yes --> CreateTrip
    Reschedule -- No --> ReverseLogistics
    POD -- Yes --> Complete[FulfilmentCompleted]
    Complete --> Invoice[Generate invoice]
    Invoice --> EndDelivered([Delivered])

    classDef happy fill:#d4edda,stroke:#28a745,color:#155724;
    classDef exception fill:#f8d7da,stroke:#dc3545,color:#721c24;
    classDef terminal fill:#e2e3e5,stroke:#6c757d,color:#383d41;
    class OrderCreated,Reserve,Pick,Pack,Ready,CreateTrip,AssignVehicle,Load,Depart,EnRoute,Arrive,Complete,Invoice happy;
    class GeoJob,GeoHold,NotifyCustomer,Backorder,ReleaseInv,Refund,AbortReturn,ReverseLogistics,Failed exception;
    class Start,EndHold,EndBackorder,EndCancel,EndAbort,EndDelivered terminal;
`

// seedDefaultProcesses inserts the example on-demand fulfilment Process if
// no row with its code exists. No-op once seeded.
func (s *Seeder) seedDefaultProcesses(ctx context.Context) error {
	var existingID string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM msg_processes WHERE code = $1`, exampleProcessCode).Scan(&existingID)
	if err == nil {
		return nil // already seeded — leave operator edits alone
	}

	p, err := process.New(exampleProcessCode, exampleProcessName)
	if err != nil {
		return fmt.Errorf("build example process: %w", err)
	}
	desc := exampleProcessDesc
	p.Description = &desc
	p.Source = process.SourceCode
	p.Body = exampleProcessBody
	p.Tags = []string{"example", "fulfilment", "platform"}

	now := time.Now().UTC()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO msg_processes
		     (id, code, name, description, status, source, application, subdomain,
		      process_name, body, diagram_type, tags, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		 ON CONFLICT (code) DO NOTHING`,
		p.ID, p.Code, p.Name, p.Description, string(p.Status), string(p.Source),
		p.Application, p.Subdomain, p.ProcessName, p.Body, p.DiagramType, p.Tags,
		now, now); err != nil {
		return fmt.Errorf("insert example process: %w", err)
	}
	slog.Info("seeded example on-demand fulfilment process", "code", exampleProcessCode)
	return nil
}
