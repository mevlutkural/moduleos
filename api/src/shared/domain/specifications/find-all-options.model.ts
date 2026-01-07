export interface SortOrder {
  field: string;
  order: 'ASC' | 'DESC';
}

interface FindAllOptionsParams {
  page?: number;
  limit?: number;
  orderBy?: SortOrder[];
  search?: string;
}

export class FindAllOptions {
  constructor(
    public page?: number,
    public limit?: number,
    public orderBy?: SortOrder[],
    public search?: string,
  ) {}

  public static create(params: FindAllOptionsParams): FindAllOptions {
    return new FindAllOptions(
      params.page,
      params.limit,
      params.orderBy,
      params.search,
    );
  }
}
