import { Link } from "@tanstack/react-router";
import { ShieldAlert } from "lucide-react";
import type { ReactElement, ReactNode } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";

import type { ModelPriorityRow } from "./api";

function formatTime(seconds?: number) {
  return seconds ? new Date(seconds * 1000).toLocaleString() : "—";
}

export function RoutingStatusBadge({ row }: { row: ModelPriorityRow }) {
  if (row.routing_status === "quarantined") {
    return <Badge variant="destructive">已隔离</Badge>;
  }
  if (row.routing_status === "canary") {
    return <Badge variant="outline">Canary {row.canary_percent}%</Badge>;
  }
  if (!row.enabled) return <Badge variant="destructive">禁用</Badge>;
  if (row.routing_status === "healthy") {
    return <Badge variant="secondary">健康</Badge>;
  }
  return <Badge variant="outline">观察中</Badge>;
}

export function QuarantineDetailsDialog({
  channelName,
  rows,
  trigger,
}: {
  channelName: string;
  rows: ModelPriorityRow[];
  trigger: ReactNode;
}) {
  const affected = rows.filter(
    (row) =>
      row.routing_status === "quarantined" || row.routing_status === "canary",
  );
  return (
    <Dialog>
      <DialogTrigger render={trigger as ReactElement} />
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <ShieldAlert className="size-5 text-red-500" />
            {channelName} · 隔离与恢复详情
          </DialogTitle>
          <DialogDescription>
            隔离会阻止该渠道承载对应模型；Canary
            表示正在用少量真实流量验证恢复。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          {affected.map((row) => (
            <div
              key={`${row.channel_id}|${row.model}`}
              className="space-y-2 rounded-xl border p-3"
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="font-mono font-semibold">{row.model}</span>
                <RoutingStatusBadge row={row} />
              </div>
              <p className="text-sm">
                {row.reason || row.attribution?.summary || "原因未记录"}
              </p>
              <div className="text-muted-foreground grid gap-1 text-xs sm:grid-cols-2">
                <span>隔离时间：{formatTime(row.disabled_at)}</span>
                <span>
                  下次探测：
                  {row.probing ? "正在探测" : formatTime(row.next_probe_at)}
                </span>
                <span>探测次数：{row.attempts ?? 0}</span>
                <span>健康分：{Math.round(row.health_score)} / 100</span>
              </div>
            </div>
          ))}
        </div>
        <DialogFooter showCloseButton>
          <Button
            variant="outline"
            render={<Link to="/admin/model-priority" />}
          >
            前往模型分级
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
