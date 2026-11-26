/**
 * IndexedDB layout (Dexie). One database per signed-in user on this device: `btn-offline-u-<userId>`,
 * plus one small device database (`btn-offline-device`) for the device id, the non-extractable device
 * secret and the list of users who keep offline data here.
 *
 * Only index fields are clear text (uuid, seq, status, type, dates); every payload is an encrypted Box.
 * Schema changes = a new `this.version(n).stores(...).upgrade(...)` block below — never edit an old one
 * (Dexie migrates in place, so data is kept across app updates, OFF-21).
 */
import { Dexie, type Table } from "dexie";
import type { Box, WrappedKey } from "./crypto";

export const LOCAL_SCHEMA_VERSION = 2;

export type OutboxStatus = "pending" | "synced" | "conflict" | "rejected" | "dismissed";

/** What is encrypted inside an outbox row. */
export interface OutboxSecret {
  payload: unknown;
  reason?: string | null;
  serverId?: string | null;
  serverVersion?: string | number | null;
  flags?: string[];
  /** server copy when the server answered "conflict" */
  serverData?: unknown;
}

export interface OutboxRow {
  uuid: string;
  /** monotonic per device+user; capture order */
  seq: number;
  /** bumped when the user corrects and resubmits a rejected/conflict record */
  revision: number;
  type: string;
  schemaVersion: number;
  userId: string;
  deviceId: string;
  /** ISO timestamp from the device clock */
  capturedAt: string;
  status: OutboxStatus;
  /** domain key of the local record this event updates, e.g. "plant|2026-11-23|worker" */
  key: string;
  attempts: number;
  lastAttemptAt: string | null;
  syncedAt: string | null;
  reasonCode: string | null;
  box: Box;
}

export interface LocalRecordRow {
  type: string;
  key: string;
  /** last outbox uuid that touched this record, if any */
  uuid: string | null;
  status: OutboxStatus | "server";
  /** business date (YYYY-MM-DD) used for the 60-day scope */
  date: string | null;
  updatedAt: string;
  box: Box;
}

export interface RefRow {
  entity: string;
  id: string;
  version: string | number | null;
  date: string | null;
  box: Box;
}

export interface CacheRow {
  key: string;
  savedAt: string;
  box: Box;
}

export interface MetaRow {
  key: string;
  value: unknown;
}

export interface KeySlot extends WrappedKey {
  kind: "pin" | "password" | "session";
  salt: Uint8Array | null;
  iterations: number;
  createdAt: string;
  failures?: number;
}

export class UserDB extends Dexie {
  outbox!: Table<OutboxRow, string>;
  records!: Table<LocalRecordRow, [string, string]>;
  ref!: Table<RefRow, [string, string]>;
  balances!: Table<RefRow, [string, string]>;
  cache!: Table<CacheRow, string>;
  meta!: Table<MetaRow, string>;
  slots!: Table<KeySlot, string>;

  constructor(name: string) {
    super(name);
    this.version(1).stores({
      outbox: "uuid, seq, status, type, [status+seq]",
      records: "[type+key], type, uuid, status",
      ref: "[entity+id], entity",
      cache: "key",
      meta: "key",
      slots: "kind",
    });
    // v2: balances snapshots table + business-date indexes for 60-day pruning.
    this.version(2)
      .stores({
        outbox: "uuid, seq, status, type, key, [status+seq]",
        records: "[type+key], type, uuid, status, date",
        ref: "[entity+id], entity, date",
        balances: "[entity+id], entity",
        cache: "key, savedAt",
      })
      .upgrade(async (tx) => {
        await tx.table("meta").put({ key: "schemaMigratedAt", value: new Date().toISOString() });
      });
  }
}

export const userDbName = (userId: string) => `btn-offline-u-${userId}`;

export interface DeviceUser {
  userId: string;
  name: string;
  email: string;
  lastOnlineAt: string;
  /** offline unlock window end (clear text so the unlock screen can say until when) */
  expiresAt?: string;
}

export class DeviceDB extends Dexie {
  kv!: Table<{ key: string; value: unknown }, string>;
  users!: Table<DeviceUser, string>;
  constructor() {
    super("btn-offline-device");
    this.version(1).stores({ kv: "key", users: "userId" });
    this.version(2).stores({ users: "userId, lastOnlineAt" });
  }
}

let deviceDb: DeviceDB | null = null;
export const getDeviceDb = () => (deviceDb ??= new DeviceDB());
