import { z } from "zod";

export const BaseEntitySchema = z.object({
  id: z.string(),
  createdAt: z.string(),
  updatedAt: z.string(),
});

export const PaginatedMetaSchema = z.object({
  totalRecords: z.number(),
  totalPages: z.number(),
  currentPage: z.number(),
  hasNextPage: z.boolean(),
  hasPreviousPage: z.boolean(),
});

export const createPaginatedListSchema = <T extends z.ZodTypeAny>(
  itemSchema: T
) =>
  z.object({
    records: z.array(itemSchema),
    meta: PaginatedMetaSchema,
  });
