import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { ModelPriorityRow } from "./api";
import { QuarantineDetailsDialog, RoutingStatusBadge } from "./routing-status";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: React.ReactNode }) => (
    <a href="/admin/model-priority">{children}</a>
  ),
}));

const quarantined: ModelPriorityRow = {
  channel_id: 7,
  channel_name: "测试渠道",
  model: "gpt-test",
  group: "default",
  weight: 0,
  enabled: false,
  base_priority: 10,
  eff_priority: 0,
  delta: -10,
  health_score: 18,
  confidence: 0.9,
  routing_status: "quarantined",
  canary_percent: 0,
  reason: "连续失败达到阈值",
  disabled_at: 1_725_000_000,
  next_probe_at: 1_725_000_600,
  attempts: 3,
};

describe("routing status visibility", () => {
  it("renders the real quarantine and canary status", () => {
    const { rerender } = render(<RoutingStatusBadge row={quarantined} />);
    expect(screen.getByText("已隔离")).toBeInTheDocument();
    rerender(
      <RoutingStatusBadge
        row={{ ...quarantined, routing_status: "canary", canary_percent: 25 }}
      />,
    );
    expect(screen.getByText("Canary 25%")).toBeInTheDocument();
  });

  it("opens a readable model-level quarantine explanation", async () => {
    const user = userEvent.setup();
    render(
      <QuarantineDetailsDialog
        channelName="测试渠道"
        rows={[quarantined]}
        trigger={<button type="button">已隔离 1</button>}
      />,
    );
    await user.click(screen.getByRole("button", { name: "已隔离 1" }));
    expect(
      screen.getByRole("heading", { name: /测试渠道 · 隔离与恢复详情/ }),
    ).toBeInTheDocument();
    expect(screen.getByText("gpt-test")).toBeInTheDocument();
    expect(screen.getByText("连续失败达到阈值")).toBeInTheDocument();
    expect(screen.getByText("探测次数：3")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "前往模型分级" })).toHaveAttribute(
      "href",
      "/admin/model-priority",
    );
  });
});
