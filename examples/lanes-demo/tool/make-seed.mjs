#!/usr/bin/env node
import { initializeApp } from "firebase-admin/app";
import { getAuth } from "firebase-admin/auth";
import { getFirestore } from "firebase-admin/firestore";

const usage = `Usage: tool/make-seed.mjs

Fill a fresh emulator session with the demo seed: 5 000 documents in items,
demo/hello and one Auth user. Run it under emulators:exec, which exports the
session into seed/ on exit, never against a session tests ran against:

  rm -rf seed
  npx firebase emulators:exec --config firebase.emulator.json --project demo-lanes \\
    --only auth,firestore --export-on-exit seed "node tool/make-seed.mjs"
`;

if (process.argv.includes("-h") || process.argv.includes("--help")) {
  process.stdout.write(usage);
  process.exit(0);
}
if (!process.env.FIRESTORE_EMULATOR_HOST || !process.env.FIREBASE_AUTH_EMULATOR_HOST) {
  process.stderr.write("make-seed.mjs: no emulator in the environment\n\n" + usage);
  process.exit(2);
}

initializeApp({ projectId: "demo-lanes" });
const db = getFirestore();

await db.doc("demo/hello").set({ greeting: "hello from the seed" });
const items = 5000;
for (let start = 0; start < items; start += 500) {
  const batch = db.batch();
  for (let i = start; i < Math.min(start + 500, items); i++) {
    const id = `item-${String(i).padStart(5, "0")}`;
    batch.set(db.collection("items").doc(id), { n: i, label: id });
  }
  await batch.commit();
}
await getAuth().createUser({ uid: "demo-user", email: "demo@example.com", password: "demo-password" });
process.stdout.write(`seeded ${items} items, demo/hello and demo-user\n`);
