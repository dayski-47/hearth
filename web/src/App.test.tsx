import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import App from "./App";

const future = { v7_startTransition: true, v7_relativeSplatPath: true } as const;

test("renders the dashboard route", () => {
  render(
    <MemoryRouter initialEntries={["/"]} future={future}>
      <App />
    </MemoryRouter>,
  );
  expect(screen.getByText("Dashboard")).toBeInTheDocument();
});

test("unknown route redirects to dashboard", () => {
  render(
    <MemoryRouter initialEntries={["/nope"]} future={future}>
      <App />
    </MemoryRouter>,
  );
  expect(screen.getByText("Dashboard")).toBeInTheDocument();
});
