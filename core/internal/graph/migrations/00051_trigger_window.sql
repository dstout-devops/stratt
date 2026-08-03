-- ADR-0162: a Trigger decides on more than one event, so the engine needs to remember the recent
-- past — durably, and shared across replicas.
--
-- WHAT THIS REPLACES IS ALREADY BROKEN. Cooldown (ADR-0018) keeps its bookkeeping in a map in one
-- process: it resets when a pod restarts, and two replicas each hold their own idea of when a
-- Trigger last fired. The storm damping an estate declares is not the storm damping it gets, and
-- nothing anywhere says so. This is a bug fix before it is a feature.
--
-- ── THIS IS NOT A PROJECTION, AND THE SEPARATION IS DELIBERATE (ADR-0162 D5) ──────────────────
--
-- It lives in its own table with NO foreign key into graph.entity and no Facet, so it can never be
-- joined into an estate query and mistaken for a fact about a host. It describes the EVENT STREAM,
-- not the estate: transient by construction (rows expire), rebuildable by definition (derived from
-- events that are themselves durable on JetStream), and no Entity attribute is ever written from it.
-- §1.2 governs facts about the estate; this is the engine's memory of its own recent past, in the
-- same category as a Temporal timer.
--
-- The line to hold: if "this host has flapped five times" should ever be queryable, that is a Facet
-- with a Normalizer and provenance — never a widened version of this table.
--
-- ── AND IT MAKES THE DECISION READABLE, WHICH A RULES ENGINE'S MEMORY IS NOT ──────────────────
--
-- "Why did this Trigger not fire?" becomes answerable: the window, the count so far, and the last
-- fire are rows an operator can be shown (§1.8). That is the parity argument turned around — the
-- same behaviour as AAP's working memory, with diagnosis it cannot offer.

-- +goose Up
CREATE TABLE graph.trigger_window (
    -- The Trigger this window belongs to. TEXT rather than a foreign key: a Trigger is Git-declared
    -- desired state (§1.2) and may be renamed or removed between reconciles, and a window that
    -- blocked a declaration change would make the engine's memory authoritative over the estate.
    trigger_name text NOT NULL,
    -- The correlation value tying events together (ADR-0162 D4), or '' for a Trigger that correlates
    -- on nothing — which is every count/cooldown Trigger. Part of the key so two services' events
    -- never share a window.
    correlation_key text NOT NULL DEFAULT '',
    -- opened_at is when this window started; it is what `withinSeconds` is measured from, and
    -- resetting the window means deleting the row rather than mutating this.
    opened_at timestamptz NOT NULL DEFAULT now(),
    -- How many MATCHING events have landed in this window (ADR-0162 D3).
    match_count integer NOT NULL DEFAULT 0,
    -- Which of an AllOf Trigger's conditions have been satisfied, by INDEX into the declared list
    -- (ADR-0162 D4). Indexes rather than the expression text: an author editing one condition's
    -- wording must not silently satisfy a different slot, and the declared ORDER is the only stable
    -- identity a condition has.
    satisfied integer[] NOT NULL DEFAULT '{}',
    -- last_fired_at is the cooldown fact, moved here from a process's memory. NULL ⇒ never fired.
    last_fired_at timestamptz,
    PRIMARY KEY (trigger_name, correlation_key)
);

-- Expiry sweeps by age, and firing deletes by key — both want this.
CREATE INDEX trigger_window_opened_idx ON graph.trigger_window (opened_at);

COMMENT ON TABLE graph.trigger_window IS
    'ADR-0162: the Trigger engine''s memory of its own recent past — windows, counts and cooldowns. Describes the EVENT STREAM, never the estate: no Facet, no Entity reference, nothing here is a fact about a host (§1.2).';

-- +goose Down
DROP TABLE IF EXISTS graph.trigger_window;
