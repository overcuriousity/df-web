-- web-units: slim citizen roster for the /play sidebar Dwarves tab.
-- Invoked via: dfhack-run web-units
-- Output: JSON array. The richer DT-style payload lives in web-units-full.

local json = require('json')

local function safe_str(s) return dfhack.df2utf(s or '') end

local function happiness_bucket(stress)
    if not stress then return 'Unknown' end
    if stress <= -10000 then return 'Ecstatic'
    elseif stress <= -5000 then return 'Happy'
    elseif stress <= -1000 then return 'Content'
    elseif stress <=   1000 then return 'Fine'
    elseif stress <=  25000 then return 'Unhappy'
    elseif stress <=  50000 then return 'Very Unhappy'
    else                        return 'Miserable'
    end
end

local function current_job_name(u)
    local ok, name = pcall(function()
        if u.job and u.job.current_job then
            return dfhack.job.getName(u.job.current_job)
        end
        return nil
    end)
    if ok and name and name ~= '' then return name end
    return 'No Job'
end

local function age(u)
    local cur = df.global.cur_year
    if u.birth_year and cur and cur >= u.birth_year then
        return cur - u.birth_year
    end
    return 0
end

local function stress_of(u)
    local ok, v = pcall(function()
        return u.status.current_soul.personality.stress
    end)
    if ok and type(v) == 'number' then return v end
    return nil
end

local result = {}
for _, u in ipairs(df.global.world.units.active) do
    if dfhack.units.isCitizen(u) and dfhack.units.isAlive(u) then
        result[#result + 1] = {
            id          = u.id,
            name        = safe_str(dfhack.units.getReadableName(u)),
            profession  = safe_str(dfhack.units.getProfessionName(u)),
            age         = age(u),
            current_job = safe_str(current_job_name(u)),
            happiness   = happiness_bucket(stress_of(u)),
        }
    end
end

print(json.encode(result))
