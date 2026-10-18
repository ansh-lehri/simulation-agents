-- Teardown for 0001_agents.up.sql. Not invoked by the Apply runner; kept as a
-- manual / CI teardown aid.

DROP INDEX IF EXISTS idx_agents_reuse;
DROP TABLE  IF EXISTS agents;
