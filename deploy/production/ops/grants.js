// Least-privilege access for the backd service user, run by provision.sh
// with admin credentials. The role "backdApp" allows, and nothing else:
//   on each collection in COLLECTIONS (<database>.<collection>, one per
//   line, from `backd databases --collections`):
//     find, insert, update, remove    the API's document operations
//     listIndexes                     PROVISION_MODE=verify's index checks
//   except:
//   - each realm's audit trail (<realm>___system.audit) and invocation
//     history (<realm>___system.invocations), both append-only: find,
//     insert and listIndexes, so not even backd's own user can change or
//     delete records (MongoDB's TTL monitor expires them);
//   - each realm's async jobs (<realm>___system.jobs): find, insert,
//     update and listIndexes, no remove — a job is only ever claimed and
//     completed in place, never deleted by backd itself (TTL expires it);
//   - backd___deployment.realms, where provisioning records the config it
//     applied: find and listIndexes only (backd in verify mode reads it)
//   on each of their databases:
//     listCollections                 verify's collection and validator checks
// Grants name collections, not whole databases, because MongoDB lets
// "insert" on a database create any collection in it. No createCollection,
// createIndex, collMod or drop*: only the provision job changes the schema.
// Collections removed from the config lose access.
//
// listCollections is granted on every database DATABASES lists (`backd
// databases`, no flag), not just the ones with a collection in COLLECTIONS:
// a functions-only database (no collections of its own, e.g. deploy/
// production/functions/netprobe/app) still gets listed and checked by
// PROVISION_MODE=verify, so it still needs the grant, even though the
// per-collection loop below never mentions it.
//
// With BACKD_BACKUP_PASSWORD set, the user "backup" gets the role
// "backdBackup": find and listIndexes on the same collections, and
// listCollections on their databases. Enough for mongodump, nothing more.
const namespaces = (process.env.COLLECTIONS || "").split("\n").filter(Boolean);
const allDatabases = (process.env.DATABASES || "").split("\n").filter(Boolean);
const user = process.env.BACKD_APP_USER;
const pwd = process.env.BACKD_APP_PASSWORD;
if (namespaces.length === 0) throw new Error("COLLECTIONS is empty");
if (allDatabases.length === 0) throw new Error("DATABASES is empty");

const privileges = [];
const databases = new Set();
for (const ns of namespaces) {
  const dot = ns.indexOf(".");
  const [database, collection] = [ns.slice(0, dot), ns.slice(dot + 1)];
  databases.add(database);
  const appendOnly = database.endsWith("___system") && (collection === "audit" || collection === "invocations");
  const noRemove = database.endsWith("___system") && collection === "jobs";
  const readOnly = database === "backd___deployment";
  privileges.push({
    resource: { db: database, collection },
    actions: readOnly ? ["find", "listIndexes"]
      : appendOnly ? ["find", "insert", "listIndexes"]
      : noRemove ? ["find", "insert", "update", "listIndexes"]
      : ["find", "insert", "update", "remove", "listIndexes"],
  });
}
for (const database of allDatabases) databases.add(database);
for (const database of databases) {
  privileges.push({ resource: { db: database, collection: "" }, actions: ["listCollections"] });
}

const admin = db.getSiblingDB("admin");

// setRole creates or replaces a role; setUser creates or updates a user
// with exactly that role.
function setRole(role, privileges) {
  if (admin.getRole(role)) admin.updateRole(role, { privileges, roles: [] });
  else admin.createRole({ role, privileges, roles: [] });
}
function setUser(name, pwd, role) {
  const roles = [{ role, db: "admin" }];
  if (admin.getUser(name)) admin.updateUser(name, { pwd, roles });
  else admin.createUser({ user: name, pwd, roles, mechanisms: ["SCRAM-SHA-256"] });
}

setRole("backdApp", privileges);
setUser(user, pwd, "backdApp");
print(`user ${user}: document access to ${namespaces.length} collections; listCollections on ${databases.size} databases`);

if (process.env.BACKD_BACKUP_PASSWORD) {
  setRole("backdBackup", privileges.map((p) => ({
    resource: p.resource,
    actions: p.resource.collection === "" ? ["listCollections"] : ["find", "listIndexes"],
  })));
  setUser("backup", process.env.BACKD_BACKUP_PASSWORD, "backdBackup");
  print(`user backup: read access to the same collections`);
}
