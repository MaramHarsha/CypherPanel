import { defineConfig } from "orval";

// The generated client is the ONLY way the UI talks to the API (ENGINEERING
// rule 25; tech-stack.md). Source of truth: core/api/rest/openapi.yaml.
export default defineConfig({
  cypherpanel: {
    input: { target: "../core/api/rest/openapi.yaml" },
    output: {
      target: "src/api/gen/endpoints.ts",
      schemas: "src/api/gen/model",
      client: "react-query",
      httpClient: "fetch",
      mode: "tags-split",
      clean: true,
      override: {
        mutator: { path: "src/api/client.ts", name: "apiFetch" },
        // apiFetch returns the parsed body directly; don't wrap responses in
        // a {data,status} envelope type.
        fetch: { includeHttpResponseReturnType: false },
      },
    },
  },
});
