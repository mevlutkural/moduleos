import { FindAllOptions, SortOrder } from './find-all-options.model';

interface TimeRangedFindAllOptionsParams {
  page?: number;
  limit?: number;
  orderBy?: SortOrder[];
  search?: string;
  startDate?: Date;
  endDate?: Date;
}

export class TimeRangedFindAllOptions extends FindAllOptions {
  startDate?: Date;
  endDate?: Date;

  private constructor(
    page?: number,
    limit?: number,
    orderBy?: SortOrder[],
    search?: string,
    startDate?: Date,
    endDate?: Date,
  ) {
    super(page, limit, orderBy, search);
    this.startDate = startDate;
    this.endDate = endDate;
  }

  public static create(
    params: TimeRangedFindAllOptionsParams,
  ): TimeRangedFindAllOptions {
    return new TimeRangedFindAllOptions(
      params.page,
      params.limit,
      params.orderBy,
      params.search,
      params.startDate,
      params.endDate,
    );
  }
}
