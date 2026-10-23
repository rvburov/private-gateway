import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { HelpTip } from "./HelpTip";

afterEach(cleanup);

describe("compact help", () => {
  it("opens on a tap and exposes the related explanation to assistive technology", () => {
    render(<HelpTip label="О замерах скорости"><p>Скорость измеряется до Cloudflare.</p></HelpTip>);
    const button = screen.getByRole("button", { name: "О замерах скорости" });
    expect(button.getAttribute("type")).toBe("button");
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("Скорость измеряется до Cloudflare.")).toBeNull();

    fireEvent.click(button);
    const explanation = screen.getByRole("note", { name: "О замерах скорости" });
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(button.getAttribute("aria-controls")).toBe(explanation.id);
    expect(explanation.textContent).toContain("Скорость измеряется до Cloudflare.");

    fireEvent.click(button);
    expect(screen.queryByRole("note")).toBeNull();
    expect(button.getAttribute("aria-expanded")).toBe("false");
  });

  it("closes with Escape and keeps keyboard focus on its button", () => {
    render(<HelpTip label="О показателях"><p>Подробности проверки.</p></HelpTip>);
    const button = screen.getByRole("button", { name: "О показателях" });
    button.focus();
    fireEvent.click(button);
    fireEvent.keyDown(button, { key: "Escape" });
    expect(screen.queryByRole("note")).toBeNull();
    expect(document.activeElement).toBe(button);
    expect(button.getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps help open while reading it and closes when tapping outside", () => {
    render(<><HelpTip label="О замерах"><p>Подробности замера.</p></HelpTip><button>Другой раздел</button></>);
    const button = screen.getByRole("button", { name: "О замерах" });
    fireEvent.click(button);
    fireEvent.pointerDown(screen.getByText("Подробности замера."));
    expect(screen.queryByRole("note")).not.toBeNull();
    const outside = screen.getByRole("button", { name: "Другой раздел" });
    outside.focus();
    fireEvent.pointerDown(outside);
    expect(screen.queryByRole("note")).toBeNull();
    expect(document.activeElement).toBe(outside);
  });
});
