-- ADR-0157 D3 needs the INHERITED View, and nothing persisted it.
--
-- D3 says a cancel is authorized by "the `runner` grant on every actuation Step's View — exactly
-- the check `api.authorizeLaunch` already applies at the launch door". At the LAUNCH door that
-- works: the caller either launched directly (every actuation Step names its own View) or launched
-- from a Finding (the Baseline supplies the View, passed in as the default the Steps inherit).
--
-- At the CANCEL door it does not, and the ADR did not notice. A Finding-launched DAG's Steps omit
-- `viewName` on purpose (ADR-0151: the Assignment says WHERE, the recipe does not), the inherited
-- value rode DAGInput into Temporal, and `graph.workflow_run` kept no copy. So the cancel handler
-- has a Workflow spec whose actuation Steps name no View and no default to supply — and
-- authorizeLaunch correctly refuses that with a 409, which would make the most ordinary cancel
-- (stop a remediation that is sitting on a Gate) fail as a malformed Workflow.
--
-- Deriving it from the child Runs' `view_ref` was the alternative and it fails in exactly that
-- case: a DAG blocked on its Gate has no child Run yet, and a Gate is where a cancel most often
-- arrives. An authorization input that exists only once execution is underway cannot gate an
-- operation whose whole purpose is to stop execution.
--
-- It also closes a §1.8 gap that predates this ADR: the row for a remediation-launched DAG could
-- not say which View it targeted, so the descent had to reconstruct it from a child Run.
--
-- EXPAND-HALF SAFE (ADR-0078): a nullable column with no default. The previous release's replicas
-- neither write nor read it, and every existing row keeps NULL — which reads as "the Steps name
-- their own Views", the behaviour that was already correct for direct launches.

-- +goose Up
ALTER TABLE graph.workflow_run ADD COLUMN view_name text;

COMMENT ON COLUMN graph.workflow_run.view_name IS
    'The View this execution INHERITED at launch (ADR-0151), which its actuation Steps use when they name none. NULL for a direct launch, where each Step names its own. Authorization input for cancel (ADR-0157 D3).';

-- +goose Down
ALTER TABLE graph.workflow_run DROP COLUMN view_name;
