# Role: Security & Privilege

Attack trust and authority boundaries around money/control publication.

Focus on:
- caller-supplied identifiers, timestamps, thresholds, windows, programme IDs, or evidence that should be re-derived from durable authority;
- PostgreSQL roles, inheritance, owner/BYPASSRLS, column grants, security-barrier views, RLS scope, and raw SQL escape routes;
- whether a data-plane role can manufacture or prolong a money-gate state;
- privilege cutover/revoke/rollback ordering and mixed-version fleets;
- replay, provenance, signing/authentication boundaries, secret handling, and config integrity;
- structural fences that guard only one method name while a raw SQL or alternate writer bypass remains.

Do not claim compromise of a role implies arbitrary superuser capability unless the supplied source proves that authority. Distinguish role-level risk from hypothetical host compromise.
