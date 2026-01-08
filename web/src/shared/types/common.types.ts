export type Nullable<T> = T | null;
export type Optional<T> = T | undefined;
export type ID = string;

export type PartialBy<T, K extends keyof T> = Omit<T, K> & Partial<Pick<T, K>>;

export type RequiredBy<T, K extends keyof T> = Omit<T, K> &
  Required<Pick<T, K>>;

export type ValueOf<T> = T[keyof T];

export interface BaseEntity {
  id: ID;
  createdAt: string;
  updatedAt: string;
}

export interface SortParam {
  field: string;
  direction: "asc" | "desc";
}

export interface QueryParams {
  page?: number;
  limit?: number;
  search?: string;
  startDate?: Date;
  endDate?: Date;
  orderBy?: SortParam[];
}
