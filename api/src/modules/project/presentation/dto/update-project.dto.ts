import { IsNotEmpty, IsOptional, IsString, MaxLength } from 'class-validator';

export class UpdateProjectDto {
  @IsString()
  @IsNotEmpty()
  @MaxLength(255)
  name: string;

  @IsString()
  @IsOptional()
  @MaxLength(2000)
  description?: string | null;
}
