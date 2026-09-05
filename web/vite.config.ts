import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],

  server: {
    port: 5173,
    // The API is proxied rather than called cross-origin in development.
    //
    // The refresh token is an HttpOnly cookie scoped to /api/v1/auth. Calling
    // http://localhost:8080 directly from http://localhost:5173 makes every
    // request cross-site, which means the cookie needs SameSite=None and
    // therefore Secure - so it would not be sent over plain HTTP at all, and
    // sessions would silently never survive a reload. Same-origin in
    // development keeps the cookie behaving as it does in production.
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", changeOrigin: false },
    },
  },

  build: {
    // Source maps for a production build. They are served alongside the
    // bundle, which means anyone can read the source - and that is already
    // true of any frontend. What they buy is a stack trace in an error report
    // that names a function instead of a minified letter.
    sourcemap: true,
  },
});
