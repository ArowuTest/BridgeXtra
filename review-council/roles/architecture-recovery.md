# Role: Architecture & Failure Recovery

Review the candidate as a distributed control system that crashes at arbitrary points.

Attack:
- crash after external/control side effect but before terminal bookkeeping;
- retry and reclaim after partial success;
- duplicate execution vs idempotent/fence-aware side effects;
- worker death, process restart, network partition, DB timeout, and delayed publication;
- whether missed/overdue can still be derived when every scheduler worker is dead;
- whether one hung/failed tenant blocks siblings;
- rollback of DB privileges and mixed-version fleet behavior;
- whether manual execution can erase scheduled failure history;
- whether operational alerts depend on the component whose death they are supposed to detect.

Do not redesign unrelated services. Classify non-current activation/cutover risks into the correct later tranche.
