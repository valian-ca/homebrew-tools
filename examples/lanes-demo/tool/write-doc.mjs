#!/usr/bin/env node
import { initializeApp } from "firebase-admin/app";
import { getFirestore } from "firebase-admin/firestore";

const usage = `Usage: tool/write-doc.mjs [id]

Write written/<id> (default: from-shell) into the Firestore that
FIRESTORE_EMULATOR_HOST names, and print its path. It refuses without the
variable, so it never reaches a real project.

  eval "$(lanes env)" && node tool/write-doc.mjs
`;

if (process.argv.includes("-h") || process.argv.includes("--help")) {
  process.stdout.write(usage);
  process.exit(0);
}
if (!process.env.FIRESTORE_EMULATOR_HOST) {
  process.stderr.write("write-doc.mjs: FIRESTORE_EMULATOR_HOST is not set\n\n" + usage);
  process.exit(2);
}

const id = process.argv[2] ?? "from-shell";
initializeApp({ projectId: "demo-lanes" });
const ref = getFirestore().collection("written").doc(id);
await ref.set({ at: new Date().toISOString(), host: process.env.FIRESTORE_EMULATOR_HOST });
process.stdout.write(`${ref.path}\n`);
process.exit(0);
