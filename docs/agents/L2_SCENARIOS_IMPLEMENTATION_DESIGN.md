Layer 2 — Scenario Parameter-Manipulation (Implementation Design)

What this doc is. The build specification for the scenario parameter/lifecycle manipulation machinery — the code that turns a scenario name into shifted generation params (EffParams) and shifted lifecycle-curve params (curve_eff) that feed mintAgent / generation. This is part of the Tier‑2 generation path, not a separate optional feature: generateOnDemand calls into it, and mintAgent/drawTraits consume its output. It is split into its own doc only for size; L2_TIER2_IMPLEMENTATION_DESIGN Point 2 is its caller and links here.

What "scenarios won't be coded" means (so this isn't mistaken for out-of-scope). It means no if-else inducement — no if scenario==thin { force thin book }. It does not mean "no scenario mechanism." This param/lifecycle manipulation is the mechanism, and it is exactly how a scenario is realized without if-else: shift the dials, draw a differently-shaped crowd, let the market character emerge. This doc is in scope and required for the scenario feature.

Grounding. The design is L2_SCENARIOS_DESIGN (concept, the link-space math, the nine surfaces, the registry) — read it first. The consumer is L2_TIER2_IMPLEMENTATION_DESIGN Point 2 (generateOnDemand → scenario.Effective; mint/drawTraits take *EffParams) and Point 5 (curve_eff into sessions:active). Baseline params come from LAYER_2_AGENT_SCHEMA + the Tier‑4 perception-generation latents. Mechanism is designed; all numeric values (intensities s, targets, bimodal c/w) are calibration slots, never fixed here.

Scope. In: the ScenarioEngine and its Effective(...) entry point, the registry loader, the baseline→moments→link transform, η computation, the merge/shift engine (shove + invert + switches), the link rulers as functions, the bimodal-φ hook, and the EffParams/curve_eff output shapes. Out: the trait draw itself (drawTraits — Tier‑2/POPULATOR/SCHEMA), the 3b curve builder (consumes curve_eff, Tier‑3), the s-fitting/measurement tool (calibration-time, L2_SCENARIOS_DESIGN surface 9), and the deferred items (theta_add, channel→trait map).

Point 0 — Where this plugs into Tier 2 (the seam, restated)
generateOnDemand(country, ex, N, rng, scenarioName):        [Tier-2 impl Point 2]
    eff, curveEff := o.scenario.Effective(country, scenarioName)   ← THIS DOC
    ... lease / load / mint, each calling drawTraits(rng, eff, ctx) ...
    (curveEff carried to sessions:active in continueBringUp Point 5 → 3b curve builder)

scenarioName == "" (no scenario / normal on-demand) → Effective returns baseline EffParams + baseline curve_eff (identity; byte-identical to pre-scenario behavior). Canonical never calls this (no scenario).

Point 1 — ScenarioEngine + Effective (the entry point)

What it is. The one object generation calls. Built once at L2 startup (holds the loaded registry + the precomputed baseline link-space + η). Effective is pure computation, runs once per on-demand bring-up, no network.

The implementation.

go
type ScenarioEngine struct {
    registry   ScenarioRegistry    // name → blob (Point 2)
    baseline   BaselineLinkSpace   // u0 + η per knob, precomputed at startup (Point 3)
    knobs      KnobCatalogue       // name → ruler (Point 3)
    params     GenParams           // baseline GenParams (for the no-scenario identity return)
}

// Effective: scenario name → shifted generation params + shifted lifecycle-curve params.
func (e *ScenarioEngine) Effective(country, name string) (*EffParams, *CurveEff) {
    if name == "" {
        return nil, nil            // nil = baseline; drawTraits(eff==nil) uses baseline params, curve builder uses baseline
    }
    blob := e.registry.Get(name)   // validated at intake already (unknown name rejected there)
    uEff   := e.shove(blob)        // Point 5: u_eff[i] = u0[i] + s·d_i·η_i  (per knob, per ruler)
    eff    := e.invert(uEff, blob) // Point 5: link⁻¹ → moments → GenParams; apply switches (incl. phi_bimodal flag)
    curve  := e.curveFrom(uEff)    // the lifecycle-curve subset of the shifted params
    return eff, curve
}

Decisions. Effective returning (nil, nil) for the empty scenario is the identity path — keeps the no-scenario on-demand path free of any scenario cost. [decided here]

Point 2 — The scenario registry (loader)

What it is. Name → {direction d, intensity s, switches, target}. A startup-loaded config artifact (not a runtime DB table — these are design-time constants). One global, market-agnostic registry; read-only at runtime. (Full structure: L2_SCENARIOS_DESIGN surface 2.)

The implementation.

go
type Direction map[string]float64   // knob-name → signed weight (the arrows). disposition uses toward/away encoding.
type ScenarioBlob struct {
    Direction Direction
    Intensity float64                // s — a CALIBRATION slot (fit to Target); placeholder until calibrated
    Switches  map[string]any         // absolute sets: tipper, min_noise_floor, phi_bimodal{c,w,spread}
    Target    MetricTarget           // metric + relation + value-slot + unit (defines the scenario; drives s-fitting)
}
type ScenarioRegistry map[string]ScenarioBlob

func LoadRegistry(path string) (ScenarioRegistry, error) { /* parse config at startup; validate knob names against catalogue */ }
func (r ScenarioRegistry) Get(name string) ScenarioBlob  { return r[name] }   // presence checked at BRING_UP intake

Decisions. Knob names in Direction are validated against the knob catalogue (Point 3, owned by the baseline layer) at load — a typo fails fast at startup, not silently at draw time. [decided here] Intensity/Target.Value/bimodal params are slots; the engine runs with whatever is configured (fit via the calibration loop, L2_SCENARIOS_DESIGN §7).

Point 3 — Baseline link-space + η + the knob catalogue (the transform layer)

What it is. The "normal crowd" every scenario shoves away from, re-expressed in the coordinates the shove happens in: u0 (baseline moment-handles in link space) and η (each knob's baseline spread, in link space). Plus the knob catalogue (name → ruler). Computed once at startup from the existing baseline GenParams (deterministic → cached). Reads the existing baseline; never owns/copies it.

The implementation.

go
type Ruler int
const ( Logit Ruler = iota; Log; Arctanh; LogRatio )   // the four rulers (L2_SCENARIOS_DESIGN §4.4)

type KnobCatalogue map[string]Ruler   // "aggressiveness.mean"→Logit, "capital.median"→Log, "phi.center"→Arctanh, "disposition"→LogRatio, "presence.*"→Log, ...

type BaselineLinkSpace struct {
    u0 map[string]float64   // baseline handle in link space, per knob
    eta map[string]float64  // baseline spread in link space ("one crowd-width"), per knob
}

// built once at startup from baseline GenParams:
func BuildBaseline(p GenParams, cat KnobCatalogue, rng *RNG) BaselineLinkSpace {
    b := BaselineLinkSpace{u0: map[string]float64{}, eta: map[string]float64{}}
    for knob, ruler := range cat {
        moment := momentOf(p, knob)              // raw dist params → interpretable moment (mean/median/center/proportions)
        b.u0[knob] = toLink(ruler, moment)       // apply the ruler: logit/log/arctanh/logratio
        b.eta[knob] = empiricalEta(p, knob, ruler, rng)  // sample baseline dist → push through ruler → stddev
    }
    return b
}

Decisions. η is empirical (sample the baseline distribution, push each draw through the ruler, take the std-dev) — uniform across all four rulers, computed once at startup, cached (L2_SCENARIOS_DESIGN surface 3). Baseline stays raw params + transform-on-demand; the non-scenario path never touches this layer. The knob catalogue lives here (a ruler is a property of the knob); the registry references its names.

Point 4 — The link rulers (pure functions)

What it is. The four domain-safe transforms and their inverses. Every continuous knob is shoved identically on its ruler; the ruler is its only individuality.

The implementation.

go
func toLink(r Ruler, m float64) float64 {
    switch r {
    case Logit:   return math.Log(m / (1 - m))            // (0,1) → ℝ
    case Log:     return math.Log(m)                      // (0,∞) → ℝ
    case Arctanh: return 0.5 * math.Log((1+m)/(1-m))      // (-1,1) → ℝ
    }
    // LogRatio handled vector-wise in shove/invert (disposition), not scalar here
}
func fromLink(r Ruler, u float64) float64 {
    switch r {
    case Logit:   return 1 / (1 + math.Exp(-u))           // ℝ → (0,1), in-range by construction (NO clamp)
    case Log:     return math.Exp(u)                      // ℝ → (0,∞)
    case Arctanh: return math.Tanh(u)                     // ℝ → (-1,1)
    }
}

Decisions. Domain-safety replaces clamping: the inverse always lands in-range, so no clamp anywhere (this realizes L2_SCENARIOS_DESIGN §3.1 / the §5.4 refinement). LogRatio (disposition) is vector-valued — shove the log-ratios, softmax-renormalize back — handled in Points 5, not as a scalar ruler.

Point 5 — The merge / shift engine (shove → invert → switches)

What it is. The heart: u_eff = u0 + s·d·η per knob, invert back to real GenParams, apply switches. Produces EffParams (→ generation) + CurveEff (→ 3b).

The implementation.

go
func (e *ScenarioEngine) shove(blob ScenarioBlob) map[string]float64 {
    uEff := map[string]float64{}
    for knob, u0 := range e.baseline.u0 {
        d := blob.Direction[knob]                 // 0 if this knob isn't in the scenario → untouched
        uEff[knob] = u0 + blob.Intensity*d*e.baseline.eta[knob]   // s·d·η, in link space
    }
    // disposition (LogRatio): shove its log-ratio components by s·d·η, defer renormalize to invert()
    return uEff
}

func (e *ScenarioEngine) invert(uEff map[string]float64, blob ScenarioBlob) *EffParams {
    eff := &EffParams{}
    for knob, u := range uEff {
        moment := fromLink(e.knobs[knob], u)          // link⁻¹ (scalar knobs)
        setMoment(eff, knob, moment)                  // moment → raw dist param (method-of-moments)
    }
    renormalizeDisposition(eff, uEff)                 // softmax the shoved log-ratios → simplex summing to 1
    applySwitches(eff, blob.Switches)                 // tipper / min_noise / phi_bimodal flag (absolute set)
    return eff
}

Decisions. Untouched knobs (d==0) pass through at baseline → a sparse scenario only moves what it names. Disposition inverts via softmax-renormalize (stays on the simplex). applySwitches sets the phi_bimodal flag + its {c,w,spread} onto EffParams — the generator reads it (Point 6). All numeric magnitudes are from the blob (calibration). [all per L2_SCENARIOS_DESIGN §3, §4, surface 4]

Point 6 — EffParams output + the bimodal-φ generator hook

What it is. The shape generation consumes, and the one place a scenario reaches into mintAgent's draw (the φ bimodal branch).

The implementation.

go
type EffParams struct {
    GenParams                       // the full shifted generation params (same type drawTraits already takes)
    PhiBimodal *BimodalPhi          // nil = normal single-hump φ; non-nil = two-camp draw
}
type BimodalPhi struct { C, W, Spread float64 }   // camp separation, bull/bear split, within-camp spread (all calibration)

// inside drawTraits (Tier-2 generation), the ONLY scenario-aware branch:
func drawPhi(rng *RNG, eff *EffParams) float64 {
    if eff == nil || eff.PhiBimodal == nil {
        return drawNormalPhi(rng, eff)                         // single hump (baseline or shifted center)
    }
    bp := eff.PhiBimodal
    if coin(rng, bp.W) { return drawCampPhi(rng, +bp.C, bp.Spread) }   // bull camp
    return drawCampPhi(rng, -bp.C, bp.Spread)                          // bear camp
}

Decisions. EffParams embeds GenParams so drawTraits(eff.GenParams) is unchanged for every trait except φ, which checks eff.PhiBimodal — the single, flag-gated scenario branch in the generator (L2_SCENARIOS_DESIGN §5). Every other trait is scenario-blind; it just receives shifted numbers. drawNormalPhi/drawCampPhi both draw on the arctanh ruler so φ stays in (-1,1) (no clamp).

Point 7 — CurveEff output + carry to 3b

What it is. The lifecycle-curve subset of the shift (shifted entry/step-out/done/re-entry rates). Computed here, consumed later by the 3b curve builder.

The implementation.

go
type CurveEff struct { EntryRate, StepOutRate, DoneRate, ReentryRate float64 }  // shifted rates (Log ruler)

func (e *ScenarioEngine) curveFrom(uEff map[string]float64) *CurveEff {
    return &CurveEff{
        EntryRate:   fromLink(Log, uEff["presence.entry_rate"]),
        StepOutRate: fromLink(Log, uEff["presence.stepout_rate"]),
        DoneRate:    fromLink(Log, uEff["presence.done_rate"]),
        ReentryRate: fromLink(Log, uEff["presence.reentry_rate"]),
    }
}

Decisions. CurveEff rides the 2b handover in sessions:active (Tier‑2 impl Point 5) so a restart rebuilds scenario-shaped curves, not baseline (L2_SCENARIOS_DESIGN surface 7). The 3b curve builder's math is unchanged — it just receives shifted rate constants. This is the only scenario output that reaches past 2a into the running session.

What this guarantees / defers
Guarantees: one Effective(country, name) call turns a scenario into EffParams + CurveEff; the whole on-demand population (leased-redrawn + minted) draws from the same shifted params (full coverage); domain-safe (no clamps); the generator stays scenario-blind except the one φ branch; empty scenario = baseline identity.
Calibration (not here): every s, every target value, bimodal c/w/spread, σ_idio, the per-scenario direction magnitudes (equal/Level-0 for prototype) — fit via the measurement loop (L2_SCENARIOS_DESIGN §7, surface 9).
Deferred/open: theta_add (blocked on theta_act); the channel→trait map (bounds what perception_profile.channel_weights fully contains) — both shared with Tier‑1 §7.

End of scenario implementation design. Caller: L2_TIER2_IMPLEMENTATION_DESIGN Point 2 (generation) + Point 5 (curve_eff carry). Concept/design: L2_SCENARIOS_DESIGN.