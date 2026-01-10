export interface PaginationParams {
  page: number;
  limit: number;
}

export interface SortOrder {
  field: string;
  order: 'ASC' | 'DESC';
}

export interface QueryParams extends PaginationParams {
  search?: string;
  orderBy?: SortOrder[];
}

export interface TimeRangedQueryParams extends QueryParams {
  startDate?: Date;
  endDate?: Date;
}
