// Isolate pi-acp's os.homedir-based session map during this test only.
import os from "node:os";
import { syncBuiltinESMExports } from "node:module";
if (!process.env.ACP_GO_TEST_HOME) throw new Error("Missing test home");
os.homedir = () => process.env.ACP_GO_TEST_HOME;
syncBuiltinESMExports();
