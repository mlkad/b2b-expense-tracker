import { BrowserRouter } from "react-router";

import { Providers } from "./providers";
import { Router } from "./router";

export function App() {
  return (
    // The router wraps the providers because the nuqs adapter reads from it.
    <BrowserRouter>
      <Providers>
        <Router />
      </Providers>
    </BrowserRouter>
  );
}
