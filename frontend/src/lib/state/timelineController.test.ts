// SPDX-License-Identifier: AGPL-3.0-or-later

import { get } from "svelte/store";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchNotificationTimeline } from "$lib/api/notifications";
import type { NotificationTimelineItem, NotificationTimelineResponse } from "$lib/api/types";
import { createTimelineController } from "./timelineController";

vi.mock("$lib/api/notifications", () => ({
	fetchNotificationTimeline: vi.fn(),
}));

const fetchTimeline = vi.mocked(fetchNotificationTimeline);
const comment = (id: string): NotificationTimelineItem => ({ id, type: "comment" });
const page = (
	items: NotificationTimelineItem[],
	overrides: Partial<NotificationTimelineResponse> = {}
): NotificationTimelineResponse => ({
	items,
	total: items.length,
	page: 1,
	perPage: 10,
	hasMore: false,
	...overrides,
});

afterEach(() => {
	fetchTimeline.mockReset();
});

describe("Discussion timeline refresh", () => {
	it("shows the first comment after an empty initial load", async () => {
		const first = comment("first");
		fetchTimeline.mockResolvedValueOnce(page([])).mockResolvedValueOnce(page([first]));
		const { actions, stores } = createTimelineController();

		await actions.autoLoadTimeline("discussion", 10, true);
		await actions.refreshTimeline("discussion");

		expect(get(stores.items)).toEqual([first]);
		expect(get(stores.pagination)).toMatchObject({ total: 1, hasMore: false });
	});

	it("updates replies on older loaded pages and keeps new comments in order", async () => {
		const older = comment("older");
		const newer = comment("newer");
		const newest = comment("newest");
		const reply = {
			id: "reply",
			body: "New reply",
			author: { login: "octocat", avatarUrl: "" },
		};
		const updated = { ...older, replies: [reply], replyCount: 1 };
		fetchTimeline
			.mockResolvedValueOnce(page([newer], { total: 2, perPage: 1, hasMore: true }))
			.mockResolvedValueOnce(page([older], { total: 2, page: 2, perPage: 1 }))
			.mockResolvedValueOnce(page([newest, newer, updated], { perPage: 100 }));
		const { actions, stores } = createTimelineController();

		await actions.autoLoadTimeline("discussion", 1, true);
		await actions.loadMoreTimeline("discussion", 1);
		await actions.refreshTimeline("discussion");

		expect(fetchTimeline).toHaveBeenLastCalledWith(
			"discussion",
			100,
			1,
			undefined,
			expect.any(AbortSignal)
		);
		expect(get(stores.items)).toEqual([updated, newer, newest]);
		expect(get(stores.pagination)).toMatchObject({ page: 2, perPage: 1, total: 3, hasMore: false });
	});

	it("ignores a late refresh after leaving the discussion", async () => {
		let completeRefresh!: (result: NotificationTimelineResponse) => void;
		fetchTimeline
			.mockResolvedValueOnce(page([]))
			.mockImplementationOnce(() => new Promise((resolve) => (completeRefresh = resolve)))
			.mockResolvedValueOnce(page([comment("other")]));
		const { actions, stores } = createTimelineController();

		await actions.autoLoadTimeline("discussion", 10, true);
		const refresh = actions.refreshTimeline("discussion");
		actions.reset();
		await actions.autoLoadTimeline("other", 10);
		completeRefresh(page([comment("late")]));
		await refresh;

		expect(get(stores.items)).toEqual([comment("other")]);
	});
});
