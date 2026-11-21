package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/geo"
)

// nearestTries is how many of the target provider's closest regions a
// "nearest" build tries before it gives up.
const nearestTries = 3

// NearestRegions ranks the regions of account acc by their distance from the
// location src. Both must have their ProviderAccount loaded.
func (o *Orchestrator) NearestRegions(ctx context.Context, src *models.Location, acc *models.ProviderAccount) ([]geo.Ranked, error) {
	if src.ProviderAccount == nil {
		return nil, errors.New("the location has no provider account")
	}
	from, how := geo.Locate(src.ProviderAccount.Type, src.Region, "", firstNonEmpty(src.Country, src.ProviderAccount.Country))
	if how == "" {
		return nil, fmt.Errorf("cannot place %s/%s on the map: give the location a country", src.ProviderAccount.Name, src.Region)
	}
	prov, err := o.Provider(acc)
	if err != nil {
		return nil, err
	}
	opts, err := prov.Regions(ctx)
	if err != nil {
		return nil, fmt.Errorf("regions of %s: %w", acc.Name, err)
	}
	return geo.Nearest(from, acc.Type, opts), nil
}

// CheckTarget says why an account cannot take a "nearest" build.
func CheckTarget(acc *models.ProviderAccount) error {
	if !acc.Enabled {
		return fmt.Errorf("provider account %s is disabled", acc.Name)
	}
	if acc.DefaultPlan == "" {
		return fmt.Errorf("provider account %s has no default plan", acc.Name)
	}
	return nil
}

// provisionNearest builds the request on another provider account, in its
// regions nearest to the source location, with the account's default plan.
// Everything else (capacity, profile, names, DNS, add-ons) comes from the
// source location, and servers built this way count as built for it.
func (o *Orchestrator) provisionNearest(ctx context.Context, r *jobs.Run, p AutoParams) error {
	var src models.Location
	if err := o.DB.Preload("ProviderAccount").First(&src, p.SourceLocationID).Error; err != nil {
		return fmt.Errorf("source location %d: %w", p.SourceLocationID, err)
	}
	var acc models.ProviderAccount
	if err := o.DB.First(&acc, p.TargetAccountID).Error; err != nil {
		return fmt.Errorf("provider account %d: %w", p.TargetAccountID, err)
	}
	if err := CheckTarget(&acc); err != nil {
		return err
	}
	ranked, err := o.NearestRegions(ctx, &src, &acc)
	if err != nil {
		return err
	}
	srcName := src.ProviderAccount.Name + " " + src.Region

	need := p.Count - o.keptSoFar(r)
	tried := 0
	for _, rg := range ranked {
		if need <= 0 || tried >= nearestTries {
			break
		}
		if rg.Match == "" {
			// The rest could not be placed either: their distance is unknown.
			break
		}
		tried++
		key := "near:" + rg.ID
		if r.Done(key) {
			continue
		}
		loc := src
		loc.ProviderAccountID, loc.ProviderAccount = acc.ID, &acc
		loc.Region, loc.Plan, loc.Image = rg.ID, acc.DefaultPlan, acc.DefaultImage
		loc.Country = firstNonEmpty(rg.Country, acc.Country)
		r.Step(fmt.Sprintf("nearest %d: %s %s/%s (≈ %.0f km from %s)", tried, acc.Name, rg.ID, acc.DefaultPlan, rg.Km, srcName))
		kept, err := o.tryLocation(ctx, r, &loc, need, firstNonEmpty(p.IPFamily, loc.IPFamily))
		if err != nil && ctx.Err() != nil {
			return err
		}
		if err != nil {
			r.Logf("warn", "%s/%s: %v", acc.Name, rg.ID, err)
		}
		if err := o.installKept(r, &p, kept); err != nil {
			return err
		}
		r.Mark(key)
		need -= len(kept)
	}
	if tried == 0 {
		return fmt.Errorf("no region of %s could be placed near %s", acc.Name, srcName)
	}
	if need > 0 {
		return fmt.Errorf("found %d of %d healthy servers in the %d regions of %s nearest to %s", p.Count-need, p.Count, tried, acc.Name, srcName)
	}
	return nil
}
